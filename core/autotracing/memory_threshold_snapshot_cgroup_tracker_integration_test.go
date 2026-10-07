// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

//go:build integration && linux

package autotracing

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/pod"
)

func TestCgroupTrackerKernelNotification(t *testing.T) {
	root, err := cgroups.MemoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	containerID := fmt.Sprintf("%064x", time.Now().UnixNano())
	directory := filepath.Join(root, containerID)
	err = os.Mkdir(directory, 0o700)
	if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EROFS) {
		t.Skipf("memory cgroup hierarchy is not writable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(directory); err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Errorf("remove isolated cgroup: %v", err)
		}
	})
	limitFile := "memory.limit_in_bytes"
	if cgroups.CgroupMode() == cgroups.Unified {
		limitFile = "memory.max"
	}
	if _, err := os.Stat(filepath.Join(directory, limitFile)); errors.Is(err, os.ErrNotExist) {
		t.Skip("memory controller is not enabled for child cgroups")
	}
	configure := func() {
		if err := os.WriteFile(filepath.Join(directory, limitFile), []byte("134217728"), 0o600); err != nil {
			t.Fatal(err)
		}
		if cgroups.CgroupMode() == cgroups.Unified {
			// Keep the test policy above 20% so reclaim cannot prevent crossing it.
			if err := os.WriteFile(filepath.Join(directory, "memory.high"), []byte("33554432"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	configure()
	source, err := newCgroupSource()
	if err != nil {
		t.Fatal(err)
	}
	w, err := newMemoryThresholdWatcher(t.Context(), memoryWatchOptions{ThresholdPercent: 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	tracker := fixtureCgroupTracker(source, w)
	ref := pod.ContainerRef{Key: pod.ContainerKey{ID: containerID, Generation: 1}, MemoryCgroupPath: "/" + containerID}
	if _, err := tracker.ProcessContainerEvents(t.Context(), pod.ContainerUpdate{Mode: pod.ContainerUpdateFull, Events: []pod.ContainerEvent{{Container: ref}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tracker.Close)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCgroupTrackerAllocationChild$")
	cmd.Env = append(os.Environ(), "HUATUO_MEMORY_THRESHOLD_CHILD=1")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		_ = input.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		cancel()
		if cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
	})
	if err := os.WriteFile(filepath.Join(directory, "cgroup.procs"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	observed := false
	for !observed {
		select {
		case <-w.Notifications():
		case <-ctx.Done():
			t.Fatal("kernel notification did not reach the snapshot watch interface")
		}
		events, err := w.ProcessEvents(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tracker.ProcessMemoryEvents(ctx, events, memoryEventOptions{AcceptPending: true})
		if err != nil {
			t.Fatal(err)
		}
		batch := tracker.DrainPending()
		for i := range batch {
			observation := &batch[i]
			if observation.Container == ref {
				if observation.RegistrationID == 0 || observation.Cgroup.Path != "/"+containerID {
					t.Fatalf("invalid threshold observation: %+v", observation)
				}
				if err := source.Validate(ctx, observation.Cgroup); err != nil {
					t.Fatal(err)
				}
				observed = true
			}
		}
	}
	if cgroups.CgroupMode() == cgroups.Unified {
		high, err := os.ReadFile(filepath.Join(directory, "memory.high"))
		if err != nil || strings.TrimSpace(string(high)) != "33554432" {
			t.Fatalf("watcher modified memory.high: %q, %v", high, err)
		}
	}
	oldTarget := tracker.containers[ref.Key.ID].cgroup
	oldID := tracker.containers[ref.Key.ID].registrationID
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	configure()
	// Register the replacement before the old container leaves. The watcher must
	// publish removal even when cgroupfs did not notify directory deletion.
	replacement := ref
	replacement.Key.ID = "replacement"
	update, err := tracker.ProcessContainerEvents(ctx, pod.ContainerUpdate{Mode: pod.ContainerUpdateIncremental, Events: []pod.ContainerEvent{
		{Kind: pod.ContainerCreated, Container: replacement},
	}})
	if err != nil {
		t.Fatal(err)
	}
	entry := tracker.containers[replacement.Key.ID]
	if entry == nil || entry.registrationID == 0 || entry.registrationID == oldID || oldTarget.SameInstance(entry.cgroup) ||
		len(update.Invalidated) != 0 {
		t.Fatal("same-path kernel directory replacement retained the old registration")
	}
	waitMemoryInvalidationForTest(t, tracker, oldID)
	full := pod.ContainerUpdate{Mode: pod.ContainerUpdateFull, Events: []pod.ContainerEvent{{Container: ref}, {Container: replacement}}}
	if _, err := tracker.ProcessContainerEvents(ctx, full); err != nil {
		t.Fatal(err)
	}
	if len(tracker.targets) != 1 || tracker.containers[ref.Key.ID].registrationID != 0 ||
		tracker.targets[entry.registrationID] != entry {
		t.Fatal("native removal or a full update changed the replacement registration")
	}
	update, err = tracker.ProcessContainerEvents(ctx, pod.ContainerUpdate{Mode: pod.ContainerUpdateIncremental, Events: []pod.ContainerEvent{
		{Kind: pod.ContainerDeleted, Container: ref},
	}})
	if err != nil || len(update.Invalidated) != 0 || len(tracker.targets) != 1 {
		t.Fatalf("late deletion affected the replacement: %v", err)
	}
	update, err = tracker.ProcessContainerEvents(ctx, pod.ContainerUpdate{Mode: pod.ContainerUpdateIncremental, Events: []pod.ContainerEvent{
		{Kind: pod.ContainerDeleted, Container: replacement},
	}})
	if err != nil || len(update.Invalidated) != 1 || len(tracker.targets) != 0 || len(tracker.containers) != 0 {
		t.Fatalf("container deletion did not retire the kernel registration: %v", err)
	}
}

func TestCgroupTrackerAllocationChild(t *testing.T) {
	if os.Getenv("HUATUO_MEMORY_THRESHOLD_CHILD") != "1" {
		t.Skip("allocation subprocess")
	}
	var start [1]byte
	if _, err := io.ReadFull(os.Stdin, start[:]); err != nil {
		t.Fatal(err)
	}
	memory := make([]byte, 48<<20)
	for i := 0; i < len(memory); i += 4096 {
		memory[i] = 1
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	runtime.KeepAlive(memory)
}

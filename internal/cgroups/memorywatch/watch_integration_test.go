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

package memorywatch

import (
	"context"
	"errors"
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
)

func TestWatcherKernelNotification(t *testing.T) {
	root, err := cgroups.MemoryRoot()
	if err != nil {
		t.Fatal(err)
	}
	directory, err := os.MkdirTemp(root, "huatuo-memory-watch-")
	if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EROFS) {
		t.Skipf("memory cgroup hierarchy is not writable: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(directory); err != nil {
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
	if err := os.WriteFile(filepath.Join(directory, limitFile), []byte("134217728"), 0o600); err != nil {
		t.Fatal(err)
	}
	if cgroups.CgroupMode() == cgroups.Unified {
		// Keep the test policy above 20% so reclaim cannot prevent crossing it.
		if err := os.WriteFile(filepath.Join(directory, "memory.high"), []byte("33554432"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w, err := New(Options{ThresholdPercent: 20, MaxCgroups: 1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	id, err := w.Add(ctx, "/"+filepath.Base(directory))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWatcherAllocationChild$")
	cmd.Env = append(os.Environ(), "HUATUO_MEMORY_WATCH_CHILD=1")
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
		_ = cmd.Wait()
	})
	if err := os.WriteFile(filepath.Join(directory, "cgroup.procs"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := input.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}
	var events [1]Event
	if _, err := w.ReadEvents(ctx, events[:]); err != nil {
		for _, name := range []string{"memory.current", "memory.peak", "memory.events.local", "cgroup.procs"} {
			contents, readErr := os.ReadFile(filepath.Join(directory, name))
			t.Logf("%s: %s (read error: %v)", name, contents, readErr)
		}
		t.Fatal(err)
	}
	if event := &events[0]; event.TargetID != id || event.Kind != ThresholdObserved ||
		event.UsageBytes < event.LimitBytes/5 {
		t.Fatalf("kernel notification = %+v", event)
	}
	if cgroups.CgroupMode() == cgroups.Unified {
		high, err := os.ReadFile(filepath.Join(directory, "memory.high"))
		if err != nil || strings.TrimSpace(string(high)) != "33554432" {
			t.Fatalf("watcher modified memory.high: %q, %v", high, err)
		}
	}
}

func TestWatcherAllocationChild(t *testing.T) {
	if os.Getenv("HUATUO_MEMORY_WATCH_CHILD") != "1" {
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

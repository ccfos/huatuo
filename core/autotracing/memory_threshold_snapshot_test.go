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

package autotracing

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
)

func TestNewMemoryThresholdSnapshot(t *testing.T) {
	previous := configSnapshot()
	t.Cleanup(func() { Set(previous) })
	Set(&Config{})

	attr, err := newMemoryThresholdSnapshot()
	if err != nil {
		t.Fatalf("newMemoryThresholdSnapshot() error = %v", err)
	}
	if attr == nil || attr.TracingData == nil {
		t.Fatal("memory threshold snapshots require an explicit enablement setting")
	}
}

func TestMemoryThresholdSnapshotBlacklist(t *testing.T) {
	// Disable all autotracers so this test only exercises registration.
	blacklist := []string{"cpuidle", "cpusys", "dload", "iotracing", "memburst", "memory_threshold_snapshot"}
	registered, err := tracing.NewRegister(blacklist)
	if err != nil {
		t.Fatalf("initialize blacklisted autotracers: %v", err)
	}
	if len(registered) != 0 {
		t.Fatalf("registered autotracers = %v, want none", registered)
	}
	status := tracing.EventTracingStatus()
	if got := status["memory_threshold_snapshot"]; got != "disabled" {
		t.Errorf("memory_threshold_snapshot status = %q, want disabled", got)
	}
	if _, ok := status["before_oom_memsnap"]; ok {
		t.Error("legacy before_oom_memsnap tracer is still registered")
	}
}

func TestMemorySnapshotWatchErrorPolicy(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		wait bool
	}{
		{name: "nil"},
		{name: "target limit", err: errMemoryWatchLimit},
		{name: "closed", err: errMemoryWatchClosed},
		{name: "permission", err: os.ErrPermission},
		{name: "canceled", err: context.Canceled},
		{name: "resource exhaustion", err: unix.EMFILE, wait: true},
		{name: "kernel event overflow", err: errMemoryWatchEventOverflow, wait: true},
		{name: "container registration conflict", err: errCgroupRegistrationConflict, wait: true},
		{name: "conflict during cancellation", err: errors.Join(context.Canceled, errCgroupRegistrationConflict), wait: true},
		{name: "joined cancellation", err: errors.Join(context.Canceled, unix.ENOSPC), wait: true},
		{name: "limit and resource failure", err: errors.Join(errMemoryWatchLimit, unix.EMFILE), wait: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- handleWatchError(ctx, test.err) }()
			if test.wait {
				select {
				case err := <-done:
					t.Fatalf("persistent failure allowed an immediate restart: %v", err)
				case <-time.After(10 * time.Millisecond):
				}
				cancel()
			}
			select {
			case err := <-done:
				if test.wait {
					if err != nil {
						t.Fatalf("stopping after persistent failure = %v", err)
					}
				} else if !errors.Is(err, test.err) {
					t.Fatalf("returned error = %v, want %v", err, test.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("watch error policy did not return")
			}
		})
	}
}

// Keep the real producer initializing: these tests need the subscription
// lifecycle, without requiring a kubelet, container runtime or CSS probes.
func newContainerSubscriptionForTest(t *testing.T) *pod.ContainerSubscription {
	t.Helper()
	started := make(chan struct{})
	signal := sync.OnceFunc(func() { close(started) })
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		signal()
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	port := uint32(server.Listener.Addr().(*net.TCPAddr).Port)
	if err := pod.InitManager(&pod.ManagerCtx{PodReadOnlyPort: port}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pod.ReleaseManager)
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("container producer did not begin initialization")
	}
	subscription, err := pod.SubscribeContainers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(subscription.Close)
	return subscription
}

func runMemorySnapshotForTest(t *testing.T, snapshot *memoryThresholdSnapshot,
	config *Config, source *cgroupSource, tracker *cgroupTracker, subscription *pod.ContainerSubscription,
) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- snapshot.mainAction(ctx, config, source, tracker.watcher, tracker, subscription)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("snapshot did not stop")
		}
	})
	return cancel, done
}

func TestMemoryThresholdSnapshotStopJoinsWatcherBeforeRestart(t *testing.T) {
	cfg := &Config{}
	cfg.MemoryThresholdSnapshot.ThresholdPercent = 90
	snapshot := &memoryThresholdSnapshot{}
	for iteration := 0; iteration < 2; iteration++ {
		t.Run(strconv.Itoa(iteration), func(t *testing.T) {
			subscription := newContainerSubscriptionForTest(t)
			before, err := os.ReadDir("/proc/self/fd")
			if err != nil {
				t.Fatal(err)
			}
			tracker, source := newTestCgroupTracker(t)
			createMemoryCgroupForTest(t, source.root, "/"+strings.Repeat("a", 64), 0)
			cancel, done := runMemorySnapshotForTest(t, snapshot, cfg, source, tracker, subscription)
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("snapshot did not join watcher")
			}
			if _, err := tracker.watcher.ProcessEvents(t.Context()); !errors.Is(err, errMemoryWatchClosed) {
				t.Fatalf("watcher remains active after stop: %v", err)
			}
			after, err := os.ReadDir("/proc/self/fd")
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Fatalf("open FD count changed from %d to %d", len(before), len(after))
			}
		})
	}
}

func TestMemorySnapshotChecksWatchFailureBeforeLifecycle(t *testing.T) {
	tracker, source := newTestCgroupTracker(t)
	subscription := newContainerSubscriptionForTest(t)
	subscription.Close()
	if err := tracker.watcher.Close(); err != nil {
		t.Fatal(err)
	}
	err := (&memoryThresholdSnapshot{}).mainAction(t.Context(), &Config{}, source, tracker.watcher, tracker, subscription)
	if !errors.Is(err, errMemoryWatchClosed) {
		t.Fatalf("terminal watch failure lost: %v", err)
	}
}

func TestMemorySnapshotSubscriptionClosurePreservesCooldown(t *testing.T) {
	tracker, source := newTestCgroupTracker(t)
	subscription := newContainerSubscriptionForTest(t)
	previous := time.Now()
	snapshot := &memoryThresholdSnapshot{lastAttempt: previous}
	cfg := &Config{}
	cfg.MemoryThresholdSnapshot.IntervalTracing = 300
	_, done := runMemorySnapshotForTest(t, snapshot, cfg, source, tracker, subscription)
	subscription.Close()
	select {
	case err := <-done:
		if !errors.Is(err, pod.ErrContainerSubscriptionClosed) {
			t.Fatalf("subscription closure = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("subscription closure did not stop the snapshot")
	}
	if !snapshot.lastAttempt.Equal(previous) {
		t.Fatal("subscription closure changed cooldown")
	}
}

func TestMemorySnapshotUnavailableViewSuppressesCapture(t *testing.T) {
	tracker, source := newTestCgroupTracker(t)
	path := "/" + strings.Repeat("a", 64)
	createMemoryCgroupForTest(t, source.root, path, 95)
	if err := addMemoryCgroupForTest(t, tracker, path); err != nil {
		t.Fatal(err)
	}
	id := tracker.containers[containerRefForTest(path).Key.ID].registrationID
	if _, err := tracker.ProcessMemoryEvents(t.Context(), []memoryWatchEvent{
		{Kind: memoryThresholdObserved, TargetID: id},
	}, memoryEventOptions{AcceptPending: true}); err != nil {
		t.Fatal(err)
	}
	subscription := newContainerSubscriptionForTest(t)
	ops, selected := newActionBatchOpsForTest(t)
	cfg := &Config{}
	cfg.MemoryThresholdSnapshot.ThresholdPercent = 90
	cancel, done := runMemorySnapshotForTest(t, &memoryThresholdSnapshot{captureOps: ops}, cfg, source, tracker, subscription)
	select {
	case path := <-selected:
		t.Fatalf("unavailable container view admitted capture for %s", path)
	case <-time.After(3 * arbitrationDelay):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unavailable snapshot did not stop")
	}
}

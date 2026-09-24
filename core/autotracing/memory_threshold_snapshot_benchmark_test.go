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
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups/memorywatch"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
)

// Measure event translation and candidate admission without kernel I/O or
// snapshot capture. Each group of 64 follows the production batch contract.
func BenchmarkMemorySnapshotPressureAdmission(b *testing.B) {
	for _, cooldown := range []bool{false, true} {
		for _, size := range []int{64, defaultMaxWatchedCgroups} {
			b.Run(fmt.Sprintf("cooldown=%t/targets=%d", cooldown, size), func(b *testing.B) {
				w := newCgroupTracker(nil)
				w.pending = make(map[memoryWatchRegistrationID]*cgroupWatchState, size)
				batch := make([]memorywatch.Event, size)
				for i := range batch {
					id := memorywatch.TargetID(i + 1)
					path := fmt.Sprintf("/container-%d", i)
					w.targets[id] = &cgroupWatchState{
						registrationID: id,
						cgroup:         cgroupRef{Path: path},
					}
					batch[i] = memorywatch.Event{TargetID: id, Kind: memorywatch.ThresholdObserved}
				}
				var lastAttempt time.Time
				config := &Config{}
				config.MemoryThresholdSnapshot.IntervalTracing = 3600
				actions := newActionRunner(b.Context(), config, nil, nil, &lastAttempt)
				b.Cleanup(func() { actions.Close() })
				var invalidated []memoryWatchRegistrationID
				if cooldown {
					lastAttempt = time.Now()
				}
				b.ReportAllocs()
				for b.Loop() {
					for offset := 0; offset < len(batch); offset += defaultMemoryWatchEventBatch {
						end := min(offset+defaultMemoryWatchEventBatch, len(batch))
						update, err := w.ProcessContainerEvents(b.Context(), pod.ContainerUpdate{})
						if err != nil {
							b.Fatal(err)
						}
						invalidated = append(invalidated[:0], update.Invalidated...)
						options := memoryEventOptions{AcceptPending: actions.Allowed(time.Now())}
						update, err = w.ProcessMemoryEvents(b.Context(), batch[offset:end], options)
						if err != nil {
							b.Fatal(err)
						}
						invalidated = append(invalidated, update.Invalidated...)
						actions.CancelIfInvalidated(invalidated)
					}
				}
				if (!cooldown && w.PendingCount() != size) || (cooldown && w.PendingCount() != 0) {
					b.Fatal("pending admission did not follow cooldown")
				}
			})
		}
	}
}

// Include candidate accumulation and ownership transfer to an active batch.
func BenchmarkMemorySnapshotPendingDrain(b *testing.B) {
	for _, size := range []int{64, defaultMaxWatchedCgroups} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			tracker := newCgroupTracker(nil)
			events := make([]memoryWatchEvent, size)
			for i := range events {
				id := memoryWatchRegistrationID(i + 1)
				tracker.targets[id] = &cgroupWatchState{
					registrationID: id,
					cgroup:         cgroupRef{Path: fmt.Sprintf("/container-%d", i)},
					container:      containerRefForTest(fmt.Sprintf("/container-%d", i)),
				}
				events[i] = memoryWatchEvent{Kind: memoryThresholdObserved, TargetID: id}
			}
			actions := newActionRunner(b.Context(), &Config{}, nil, nil, new(time.Time))
			b.Cleanup(func() { actions.Close() })
			active := make(map[memoryWatchRegistrationID]struct{})
			b.ReportAllocs()
			for b.Loop() {
				for offset := 0; offset < len(events); offset += defaultMemoryWatchEventBatch {
					end := min(offset+defaultMemoryWatchEventBatch, len(events))
					options := memoryEventOptions{AcceptPending: actions.Allowed(time.Now())}
					_, err := tracker.ProcessMemoryEvents(b.Context(), events[offset:end], options)
					if err != nil {
						b.Fatal(err)
					}
				}
				targets := tracker.DrainPending()
				clear(active)
				for i := range targets {
					active[targets[i].RegistrationID] = struct{}{}
				}
				if len(active) != size || len(targets) != size {
					b.Fatal("candidate handoff lost a target")
				}
				runtime.KeepAlive(targets)
			}
		})
	}
}

func BenchmarkMemorySnapshotProcessEventsIdle(b *testing.B) {
	w, _ := newTestCgroupTracker(b)
	actions := newActionRunner(b.Context(), &Config{}, nil, nil, new(time.Time))
	b.Cleanup(func() { actions.Close() })
	var invalidated []memoryWatchRegistrationID
	b.ReportAllocs()
	for b.Loop() {
		update, err := w.ProcessContainerEvents(b.Context(), pod.ContainerUpdate{})
		if err != nil {
			b.Fatal(err)
		}
		invalidated = append(invalidated[:0], update.Invalidated...)
		batch, err := processCgroupEventsForTest(w, b.Context())
		if err != nil || w.PendingCount() != 0 || len(batch.Invalidated) != 0 {
			b.Fatalf("idle processing = %+v, %v", batch, err)
		}
		invalidated = append(invalidated, batch.Invalidated...)
		actions.CancelIfInvalidated(invalidated)
	}
}

// Include both invalidation sources without registration I/O. Nonmatching IDs
// measure the full scan; the memory batch stays within the native event budget.
func BenchmarkMemorySnapshotInvalidationCheck(b *testing.B) {
	for _, size := range []int{0, 64, defaultMaxWatchedCgroups} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			containers := make([]memoryWatchRegistrationID, size)
			memory := make([]memoryWatchRegistrationID, min(size, defaultMemoryWatchEventBatch))
			for i := range containers {
				containers[i] = memoryWatchRegistrationID(i + 1)
			}
			for i := range memory {
				memory[i] = memoryWatchRegistrationID(size + i + 1)
			}
			actions := &actionRunner{active: map[memoryWatchRegistrationID]struct{}{memoryWatchRegistrationID(size + len(memory) + 1): {}}}
			var invalidated []memoryWatchRegistrationID
			b.ReportAllocs()
			for b.Loop() {
				invalidated = append(invalidated[:0], containers...)
				invalidated = append(invalidated, memory...)
				actions.CancelIfInvalidated(invalidated)
			}
		})
	}
}

func BenchmarkMemorySnapshotContainerFullUpdate(b *testing.B) {
	for _, size := range []int{0, 1, 1024, 4096} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			tracker := newCgroupTracker(nil)
			events := make([]pod.ContainerEvent, size)
			for i := range events {
				ref := containerRefForTest(fmt.Sprintf("/%064x", i))
				events[i] = pod.ContainerEvent{Container: ref}
				tracker.containers[ref.Key.ID] = &cgroupWatchState{container: ref}
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := tracker.ProcessContainerEvents(b.Context(), pod.ContainerUpdate{Mode: pod.ContainerUpdateFull, Events: events}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// Missing directories include inspection and metadata lifecycle without native watch I/O.
func BenchmarkMemorySnapshotContainerLifecycle(b *testing.B) {
	level := log.GetLevel()
	log.SetLevel("error")
	b.Cleanup(func() { log.SetLevel(level.String()) })
	for _, size := range []int{1, 64, defaultMaxWatchedCgroups} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			tracker := fixtureCgroupTracker(newTestCgroupSource(b), nil)
			b.Cleanup(tracker.Close)
			events := make([]pod.ContainerEvent, size)
			for i := range events {
				events[i].Container = containerRefForTest(fmt.Sprintf("/missing-%d", i))
			}
			full := pod.ContainerUpdate{Mode: pod.ContainerUpdateFull, Events: events}
			empty := pod.ContainerUpdate{Mode: pod.ContainerUpdateFull}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := tracker.ProcessContainerEvents(b.Context(), full); err != nil {
					b.Fatal(err)
				}
				if _, err := tracker.ProcessContainerEvents(b.Context(), empty); err != nil {
					b.Fatal(err)
				}
			}
			if len(tracker.containers) != 0 {
				b.Fatal("container removal retained ownership metadata")
			}
		})
	}
}

// Existing fixture directories include successful native watch setup and cleanup.
func BenchmarkMemorySnapshotRegistration(b *testing.B) {
	for _, size := range []int{1, 64, defaultMaxWatchedCgroups} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			tracker, source := newTestCgroupTracker(b)
			events := make([]pod.ContainerEvent, size)
			for i := range events {
				path := fmt.Sprintf("/registered-%d", i)
				createMemoryCgroupForTest(b, source.root, path, 1)
				events[i].Container = containerRefForTest(path)
			}
			full := pod.ContainerUpdate{Mode: pod.ContainerUpdateFull, Events: events}
			empty := pod.ContainerUpdate{Mode: pod.ContainerUpdateFull}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := tracker.ProcessContainerEvents(b.Context(), full); err != nil {
					b.Fatal(err)
				}
				if len(tracker.targets) != size {
					b.Fatal("container registration skipped a target")
				}
				if _, err := tracker.ProcessContainerEvents(b.Context(), empty); err != nil {
					b.Fatal(err)
				}
			}
			if len(tracker.targets) != 0 || len(tracker.containers) != 0 {
				b.Fatal("container deletion retained registrations or ownership")
			}
		})
	}
}

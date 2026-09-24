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
	"fmt"
	"io"
	"os"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/stats"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/memsnapshot"
	"github.com/ccfos/huatuo/internal/memsnapshot/collector"
	"github.com/ccfos/huatuo/internal/tracing"
)

// An empty batch isolates submission, context lifetime and completion from I/O.
func BenchmarkMemorySnapshotActionDispatch(b *testing.B) {
	runner := newActionRunner(b.Context(), &Config{}, nil, nil, new(time.Time))
	b.Cleanup(func() { runner.Close() })
	b.ReportAllocs()
	for b.Loop() {
		if err := runner.Submit(nil); err != nil {
			b.Fatal(err)
		}
		<-runner.Done()
		finished := runner.Finish()
		if !finished.IsZero() {
			b.Fatal("empty batch attempted capture")
		}
	}
}

// In-memory readings isolate candidate selection from cgroup file I/O and logging.
func BenchmarkMemorySnapshotCandidateSelection(b *testing.B) {
	for _, size := range []int{1, 64, 4096} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			batch := newActionBenchmarkBatch(b, size)
			b.ReportAllocs()
			for b.Loop() {
				if candidates, err := batch.rankCgroupTargets(b.Context()); err != nil || len(candidates) != size {
					b.Fatalf("candidate ranking failed: count=%d error=%v", len(candidates), err)
				}
			}
		})
	}
}

// Logging to io.Discard includes formatting and allocation, but not output I/O.
func BenchmarkMemorySnapshotActionBatch(b *testing.B) {
	level := log.GetLevel()
	log.SetLevel("info")
	log.SetOutput(io.Discard)
	b.Cleanup(func() {
		log.SetOutput(os.Stdout)
		log.SetLevel(level.String())
	})
	for _, size := range []int{1, 64, 4096} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			batch := newActionBenchmarkBatch(b, size)
			candidates, err := batch.rankCgroupTargets(b.Context())
			if err != nil {
				b.Fatal(err)
			}
			winner := candidates[0].observation
			source := newTestCgroupSource(b)
			createMemoryCgroupForTest(b, source.root, winner.Cgroup.Path, 100)
			winner.Cgroup = cgroupRefForTest(b, source, winner.Cgroup.Path)
			source.cgroup = batch.source.cgroup
			batch.source = source
			batch.ops = &actionBatchOps{
				validateContainer: validateActionContainerForTest,
				validateProcess:   validateActionProcessForTest,
				save:              func(*tracing.WriteRequest) error { return nil },
				selectProcess: func(context.Context, cgroupRef, uint64) (selectedProcess, error) {
					return selectedProcess{identity: memsnapshot.ProcessIdentity{TGID: 42, StartTimeTicks: 100}}, nil
				},
				captureProcessMemory: func(context.Context, memsnapshot.ProcessIdentity, collector.Options) (*collector.Result, error) {
					return &collector.Result{CaptureTime: time.Now()}, nil
				},
			}
			b.ReportAllocs()
			for b.Loop() {
				if batch.Run().IsZero() {
					b.Fatal("batch did not attempt capture")
				}
			}
		})
	}
}

func newActionBenchmarkBatch(b *testing.B, size int) actionBatch {
	b.Helper()
	manager := &benchmarkMemoryCgroup{usage: make(map[string]*stats.MemoryUsage, size)}
	targets := make([]memoryEventObservation, size)
	for i := range targets {
		path := fmt.Sprintf("/container-%04d", i)
		targets[i] = memoryEventObservation{Cgroup: cgroupRef{Path: path}, Container: containerRefForTest(path)}
		manager.usage[path] = &stats.MemoryUsage{
			Usage: uint64(900 + i*37%101), MaxLimited: 1000,
		}
	}
	config := &Config{}
	config.MemoryThresholdSnapshot.ThresholdPercent = 90
	return newActionBatch(b.Context(), config, &cgroupSource{cgroup: manager}, targets, nil)
}

type benchmarkMemoryCgroup struct {
	cgroups.Cgroup
	usage map[string]*stats.MemoryUsage
}

func (c *benchmarkMemoryCgroup) MemoryUsage(path string) (*stats.MemoryUsage, error) {
	return c.usage[path], nil
}

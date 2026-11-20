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
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/pod"
)

func TestDloadReconcileContainerLoadHistory(t *testing.T) {
	for _, changed := range []bool{false, true} {
		name := "same path"
		path := "old-cgroup"
		if changed {
			name = "changed path"
			path = "new-cgroup"
		}
		t.Run(name, func(t *testing.T) {
			lastTrace := time.Unix(100, 0)
			state := &containerDloadInfo{
				cgroupName: "old-cgroup", lastTraceAt: lastTrace,
				runnableAvg: [2]uint64{fixedOne * 20, fixedOne * 20},
				loadAvg:     [2]float64{20, 20},
				dLoadAvg:    [2]uint64{fixedOne * 50, fixedOne * 50},
				dLoad:       [2]float64{50, 50},
			}
			tracer := &dloadTracing{
				containers: map[string]*containerDloadInfo{"same-id": state},
				threshold:  dloadThreshold{load: 5, minTraceInterval: time.Minute},
			}
			container := &pod.Container{ID: "same-id", CgroupPath: path}
			tracer.reconcileContainers(map[string]*pod.Container{"same-id": container})
			if state.container != container || state.cgroupName != path {
				t.Fatal("container was not refreshed")
			}
			if !state.lastTraceAt.Equal(lastTrace) {
				t.Fatal("tracing cooldown changed")
			}
			if changed {
				if state.runnableAvg != [2]uint64{} || state.loadAvg != [2]float64{} || state.dLoadAvg != [2]uint64{} || state.dLoad != [2]float64{} {
					t.Fatalf("new cgroup retained load history: %+v", state)
				}
			} else if state.dLoad[0] != 50 || state.loadAvg[0] != 20 {
				t.Fatal("unchanged cgroup lost its history")
			}
			updateLoad(state, 0, 0)
			if got := tracer.shouldTrace(state, lastTrace.Add(2*time.Minute)); got == changed {
				t.Fatalf("shouldTrace = %v after zero-load sample, path changed = %v", got, changed)
			}
		})
	}
}

func BenchmarkDloadReconcileUnchangedPath(b *testing.B) {
	tracer := &dloadTracing{containers: make(map[string]*containerDloadInfo)}
	containers := map[string]*pod.Container{"same-id": {ID: "same-id", CgroupPath: "cgroup"}}
	tracer.reconcileContainers(containers)
	b.ReportAllocs()
	for b.Loop() {
		tracer.reconcileContainers(containers)
	}
}

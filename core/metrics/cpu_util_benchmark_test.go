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

package collector

import (
	"testing"
	"time"
)

func BenchmarkCPUUtilState(b *testing.B) {
	now := time.Unix(100, 0)
	capacity := cpuUtilCapacity(2, 1)
	cache := cpuUtilStat{lastTimestamp: now, capacity: capacity}
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		now = now.Add(time.Second)
		_, available := cache.update(cpuUtilUsage(uint64(i+1)*1_000_000), capacity, now)
		if !available {
			b.Fatal("stable sample unavailable")
		}
	}
}

// Isolate collector bookkeeping from the separately benchmarked cgroupfs reads.
func BenchmarkCPUUtilCollectorStable(b *testing.B) {
	now := time.Unix(100, 0)
	stub := &cpuUtilCgroupStub{capacity: cpuUtilCapacity(2, 1), onlineLists: make([]string, 0, 2)}
	collector := &cpuUtilCollector{cgroup: stub, now: func() time.Time { return now }}
	cache := cpuUtilStat{lastTimestamp: now, capacity: stub.capacity}
	container := cpuUtilContainer()
	b.ReportAllocs()
	b.ResetTimer()
	for i := range b.N {
		now = now.Add(time.Second)
		stub.usage = cpuUtilUsage(uint64(i+1) * 1_000_000)
		stub.onlineLists = stub.onlineLists[:0]
		metrics, err := collector.updateContainerDataCache(&cache, container, "0-7")
		if err != nil || len(metrics) != 4 {
			b.Fatalf("stable sample metrics=%d error=%v", len(metrics), err)
		}
	}
}

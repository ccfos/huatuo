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
	"math"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/stats"
)

type cpuUsageCgroup struct {
	cgroups.Cgroup
	usage stats.CpuUsage
}

func (c *cpuUsageCgroup) CpuUsage(string) (*stats.CpuUsage, error) {
	return &c.usage, nil
}

func TestCPUUtilCollectorUpdateDataCacheCounterRegression(t *testing.T) {
	tests := []struct {
		name    string
		current stats.CpuUsage
	}{
		{
			name:    "total usage regresses",
			current: stats.CpuUsage{Usage: 9, User: 6, System: 4},
		},
		{
			name:    "user usage regresses",
			current: stats.CpuUsage{Usage: 10, User: 5, System: 4},
		},
		{
			name:    "system usage regresses",
			current: stats.CpuUsage{Usage: 10, User: 6, System: 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lastTimestamp := time.Now().Add(-2 * time.Second)
			cache := cpuUtilStat{
				lastUsage:     stats.CpuUsage{Usage: 10, User: 6, System: 4},
				lastTimestamp: lastTimestamp,
				totalUtil:     11,
				usrUtil:       22,
				sysUtil:       33,
			}
			collector := cpuUtilCollector{
				cgroup: &cpuUsageCgroup{usage: tt.current},
			}

			if err := collector.updateDataCache(&cache, nil, 1); err != nil {
				t.Fatalf("updateDataCache() error = %v", err)
			}
			if cache.lastUsage != tt.current {
				t.Fatalf("last usage = %+v, want %+v", cache.lastUsage, tt.current)
			}
			if !cache.lastTimestamp.After(lastTimestamp) {
				t.Fatalf("last timestamp = %v, want after %v", cache.lastTimestamp, lastTimestamp)
			}
			if cache.totalUtil != 11 || cache.usrUtil != 22 || cache.sysUtil != 33 {
				t.Fatalf(
					"utilization changed after counter regression: total=%v user=%v system=%v",
					cache.totalUtil,
					cache.usrUtil,
					cache.sysUtil,
				)
			}
		})
	}
}

func TestCPUUtilCollectorUpdateDataCacheClampsUtilization(t *testing.T) {
	for _, tt := range []struct {
		name  string
		delta stats.CpuUsage
		cores float64
		want  [3]float64
	}{
		{"idle", stats.CpuUsage{}, 0.5, [3]float64{}},
		{"host", stats.CpuUsage{Usage: 1000000, User: 700000, System: 300000}, 4, [3]float64{12.5, 8.75, 3.75}},
		{"fractional quota", stats.CpuUsage{Usage: 500000, User: 350000, System: 150000}, 0.5, [3]float64{50, 35, 15}},
		{"slight overshoot", stats.CpuUsage{Usage: 1012860, User: 1010000}, 0.5, [3]float64{101.286, 101, 0}},
		{"user overshoot", stats.CpuUsage{Usage: 990000, User: 1010000, System: 20000}, 0.5, [3]float64{99, 101, 2}},
		{"system overshoot", stats.CpuUsage{Usage: 990000, User: 20000, System: 1010000}, 0.5, [3]float64{99, 2, 101}},
		{"combined accounting overshoot", stats.CpuUsage{Usage: 999000, User: 700000, System: 400000}, 0.5, [3]float64{99.9, 70, 40}},
		{"large overshoot", stats.CpuUsage{Usage: 3000000, User: 2000000, System: 1500000}, 0.5, [3]float64{300, 200, 150}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			backend := &cpuUsageCgroup{usage: tt.delta}
			collector := cpuUtilCollector{cgroup: backend}
			cache := cpuUtilStat{totalUtil: 11, usrUtil: 22, sysUtil: 33}
			lastTimestamp := time.Now().Add(-2 * time.Second)
			cache.lastTimestamp = lastTimestamp
			if err := collector.updateDataCache(&cache, nil, tt.cores); err != nil {
				t.Fatal(err)
			}
			if cache.lastUsage != tt.delta || !cache.lastTimestamp.After(lastTimestamp) {
				t.Fatalf("accounting baseline was not advanced: %+v", cache)
			}
			// Use the actual interval so scheduler delays cannot change expectations.
			scale := 2000000 / float64(cache.lastTimestamp.Sub(lastTimestamp).Microseconds())
			for i, got := range [3]float64{cache.totalUtil, cache.usrUtil, cache.sysUtil} {
				want := tt.want[i] * scale
				if want > 100 {
					want = 100
				}
				if math.Abs(got-want) > 1e-9 {
					t.Fatalf("field %d = %g, want %g", i, got, want)
				}
			}
		})
	}
}

func TestCPUUtilCollectorUpdateDataCacheRecoversAfterSaturation(t *testing.T) {
	// Leave seconds of scheduling margin; slight overshoots are covered above.
	backend := &cpuUsageCgroup{usage: stats.CpuUsage{Usage: 3000000, User: 2000000}}
	collector := cpuUtilCollector{cgroup: backend}
	cache := cpuUtilStat{
		lastTimestamp: time.Now().Add(-2 * time.Second),
		totalUtil:     50,
		usrUtil:       14,
		sysUtil:       36,
	}
	if err := collector.updateDataCache(&cache, nil, 0.5); err != nil {
		t.Fatal(err)
	}
	if cache.totalUtil != 100 || cache.usrUtil != 100 || cache.sysUtil != 0 {
		t.Fatalf("saturation retained previous workload values: %+v", cache)
	}
	backend.usage.Usage += 990000
	backend.usage.User += 990000
	lastTimestamp := time.Now().Add(-2 * time.Second)
	cache.lastTimestamp = lastTimestamp
	if err := collector.updateDataCache(&cache, nil, 0.5); err != nil {
		t.Fatal(err)
	}
	want := 990000 * 100 / (0.5 * float64(cache.lastTimestamp.Sub(lastTimestamp).Microseconds()))
	if math.Abs(cache.totalUtil-want) > 1e-9 || math.Abs(cache.usrUtil-want) > 1e-9 || cache.sysUtil != 0 {
		t.Fatalf("next interval included discarded CPU time or retained saturation: %+v, want %g", cache, want)
	}
	if cache.lastUsage != backend.usage {
		t.Fatalf("last usage = %+v, want %+v", cache.lastUsage, backend.usage)
	}
}

func BenchmarkCPUUtilCollectorUpdateDataCache(b *testing.B) {
	for _, usage := range []uint64{500000, 1012860} {
		name := "normal"
		if usage > 1000000 {
			name = "saturation"
		}
		b.Run(name, func(b *testing.B) {
			backend := &cpuUsageCgroup{}
			collector := cpuUtilCollector{cgroup: backend}
			cache := cpuUtilStat{}
			b.ReportAllocs()
			for b.Loop() {
				backend.usage.Usage += usage
				backend.usage.User += usage
				cache.lastTimestamp = time.Now().Add(-2 * time.Second)
				if err := collector.updateDataCache(&cache, nil, 0.5); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

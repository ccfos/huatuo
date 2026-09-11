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
	"github.com/ccfos/huatuo/internal/pod"
)

type cpuStatCgroupStub struct {
	cgroups.Cgroup
	raw   map[string]uint64
	usage stats.CpuUsage
}

func (c *cpuStatCgroupStub) CpuStatRaw(string) (map[string]uint64, error) {
	return c.raw, nil
}

func (c *cpuStatCgroupStub) CpuUsage(string) (*stats.CpuUsage, error) {
	return &c.usage, nil
}

func TestDurationNanoseconds(t *testing.T) {
	tests := []struct {
		name  string
		raw   map[string]uint64
		want  uint64
		valid bool
	}{
		{
			name:  "nanoseconds",
			raw:   map[string]uint64{"time": 42},
			want:  42,
			valid: true,
		},
		{
			name:  "microseconds",
			raw:   map[string]uint64{"time_usec": 42},
			want:  42_000,
			valid: true,
		},
		{
			name:  "nanoseconds take precedence",
			raw:   map[string]uint64{"time": 42, "time_usec": 7},
			want:  42,
			valid: true,
		},
		{
			name: "missing",
			raw:  map[string]uint64{},
		},
		{
			name: "microseconds overflow",
			raw:  map[string]uint64{"time_usec": math.MaxUint64/1000 + 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, valid := durationNanoseconds(tt.raw, "time", "time_usec")
			if got != tt.want || valid != tt.valid {
				t.Fatalf("durationNanoseconds() = (%d, %t), want (%d, %t)", got, valid, tt.want, tt.valid)
			}
		})
	}
}

func TestCalculateWaitPercent(t *testing.T) {
	tests := []struct {
		name     string
		previous cpuStat
		current  cpuStat
		want     float64
		valid    bool
	}{
		{
			name:     "normal increase",
			previous: cpuStat{waitSum: 100, cpuTotal: 100},
			current:  cpuStat{waitSum: 200, cpuTotal: 400},
			want:     25,
			valid:    true,
		},
		{
			name:     "no activity",
			previous: cpuStat{waitSum: 100, cpuTotal: 100},
			current:  cpuStat{waitSum: 100, cpuTotal: 100},
			valid:    true,
		},
		{
			name:     "wait counter reset",
			previous: cpuStat{waitSum: 100, cpuTotal: 100},
			current:  cpuStat{waitSum: 10, cpuTotal: 200},
		},
		{
			name:     "CPU counter reset",
			previous: cpuStat{waitSum: 100, cpuTotal: 100},
			current:  cpuStat{waitSum: 200, cpuTotal: 10},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, valid := calculateWaitPercent(&tt.current, &tt.previous)
			if got != tt.want || valid != tt.valid {
				t.Fatalf("calculateWaitPercent() = (%v, %t), want (%v, %t)", got, valid, tt.want, tt.valid)
			}
		})
	}
}

func TestNewCPUStatSample(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name             string
		raw              map[string]uint64
		want             cpuStat
		wantAvailability cpuStatAvailability
	}{
		{
			name: "cgroup v1",
			raw: map[string]uint64{
				"nr_throttled":   1,
				"throttled_time": 2,
				"wait_sum":       3,
				"nr_bursts":      4,
				"burst_time":     5,
			},
			want: cpuStat{
				nrThrottled:   1,
				throttledTime: 2,
				waitSum:       3,
				nrBursts:      4,
				burstTime:     5,
				cpuTotal:      6,
				lastUpdate:    now,
			},
			wantAvailability: cpuStatAvailability{
				waitPercent:   true,
				nrThrottled:   true,
				throttledTime: true,
				nrBursts:      true,
				burstTime:     true,
			},
		},
		{
			name: "cgroup v2",
			raw: map[string]uint64{
				"nr_throttled":   1,
				"throttled_usec": 2,
				"nr_bursts":      4,
				"burst_usec":     5,
			},
			want: cpuStat{
				nrThrottled:   1,
				throttledTime: 2_000,
				nrBursts:      4,
				burstTime:     5_000,
				cpuTotal:      6,
				lastUpdate:    now,
			},
			wantAvailability: cpuStatAvailability{
				nrThrottled:   true,
				throttledTime: true,
				nrBursts:      true,
				burstTime:     true,
			},
		},
		{
			name:             "missing fields",
			raw:              map[string]uint64{},
			want:             cpuStat{cpuTotal: 6, lastUpdate: now},
			wantAvailability: cpuStatAvailability{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, availability := newCPUStatSample(tt.raw, 6, now)
			if got != tt.want {
				t.Fatalf("newCPUStatSample() stat = %+v, want %+v", got, tt.want)
			}
			if availability != tt.wantAvailability {
				t.Fatalf("newCPUStatSample() availability = %+v, want %+v", availability, tt.wantAvailability)
			}
		})
	}
}

func TestCPUStatUpdateDataCacheRetainsAvailabilityOnSubSecondScrape(t *testing.T) {
	raw := map[string]uint64{
		"nr_throttled":   3,
		"throttled_usec": 4,
		"wait_sum":       100,
		"nr_bursts":      1,
		"burst_usec":     2,
	}
	container := &pod.Container{CgroupPath: "/sys/fs/cgroup/test"}
	collector := &cpuStatCollector{
		cgroup: &cpuStatCgroupStub{
			raw:   raw,
			usage: stats.CpuUsage{Usage: 1000},
		},
	}
	cache := &cpuStat{lastUpdate: time.Now().Add(-2 * time.Second)}

	first, err := collector.updateDataCache(cache, container)
	if err != nil {
		t.Fatalf("first updateDataCache() error = %v", err)
	}
	if !first.nrThrottled || !first.throttledTime || !first.nrBursts || !first.burstTime {
		t.Fatalf("first availability = %+v, want throttle/burst series", first)
	}
	if first.waitPercent {
		t.Fatal("first sample should not expose wait_sum_percent until a delta window exists")
	}

	// Simulate a second scrape inside the 1s refresh window.
	cache.lastUpdate = time.Now()
	second, err := collector.updateDataCache(cache, container)
	if err != nil {
		t.Fatalf("sub-second updateDataCache() error = %v", err)
	}
	if second != first {
		t.Fatalf("sub-second availability = %+v, want retained %+v", second, first)
	}
	if second != cache.availability {
		t.Fatalf("cache availability = %+v, want %+v", cache.availability, second)
	}
}

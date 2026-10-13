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

func TestComputeCPUUtil(t *testing.T) {
	const usagePerCorePerSecond = uint64(time.Second / time.Microsecond)

	tests := []struct {
		name     string
		prev     stats.CpuUsage
		curr     stats.CpuUsage
		elapsed  time.Duration
		numCores float64
		wantTot  float64
		wantUsr  float64
		wantSys  float64
		wantOK   bool
	}{
		{
			name:     "one core fully busy",
			prev:     stats.CpuUsage{Usage: usagePerCorePerSecond, User: 600000, System: 400000},
			curr:     stats.CpuUsage{Usage: 2 * usagePerCorePerSecond, User: 1200000, System: 800000},
			elapsed:  time.Second,
			numCores: 1,
			wantTot:  100,
			wantUsr:  60,
			wantSys:  40,
			wantOK:   true,
		},
		{
			name:     "four cores half busy",
			prev:     stats.CpuUsage{},
			curr:     stats.CpuUsage{Usage: 2 * usagePerCorePerSecond, User: usagePerCorePerSecond, System: usagePerCorePerSecond},
			elapsed:  time.Second,
			numCores: 4,
			wantTot:  50,
			wantUsr:  25,
			wantSys:  25,
			wantOK:   true,
		},
		{
			name:     "idle core",
			prev:     stats.CpuUsage{Usage: 5 * usagePerCorePerSecond, User: 3 * usagePerCorePerSecond, System: 2 * usagePerCorePerSecond},
			curr:     stats.CpuUsage{Usage: 5 * usagePerCorePerSecond, User: 3 * usagePerCorePerSecond, System: 2 * usagePerCorePerSecond},
			elapsed:  time.Second,
			numCores: 2,
			wantOK:   true,
		},
		{
			name:     "counter moved backwards",
			prev:     stats.CpuUsage{Usage: 2 * usagePerCorePerSecond, User: usagePerCorePerSecond, System: usagePerCorePerSecond},
			curr:     stats.CpuUsage{Usage: usagePerCorePerSecond, User: usagePerCorePerSecond / 2, System: usagePerCorePerSecond / 2},
			elapsed:  time.Second,
			numCores: 1,
			wantOK:   false,
		},
		{
			name:     "delta exceeds the interval and the core count",
			prev:     stats.CpuUsage{},
			curr:     stats.CpuUsage{Usage: 2 * usagePerCorePerSecond, User: usagePerCorePerSecond, System: usagePerCorePerSecond},
			elapsed:  time.Second,
			numCores: 1,
			wantOK:   false,
		},
		{
			name:     "no elapsed time",
			prev:     stats.CpuUsage{},
			curr:     stats.CpuUsage{Usage: usagePerCorePerSecond},
			numCores: 1,
			wantOK:   false,
		},
		{
			name:     "no cores",
			prev:     stats.CpuUsage{},
			curr:     stats.CpuUsage{Usage: usagePerCorePerSecond},
			elapsed:  time.Second,
			numCores: 0,
			wantOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, usr, sys, ok := computeCPUUtil(tt.prev, tt.curr, tt.elapsed, tt.numCores)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (total=%v usr=%v sys=%v)", ok, tt.wantOK, total, usr, sys)
			}
			if !tt.wantOK {
				if total != 0 || usr != 0 || sys != 0 {
					t.Fatalf("rejected sample returned total=%v usr=%v sys=%v, want all zero", total, usr, sys)
				}
				return
			}
			for _, c := range []struct {
				name string
				got  float64
				want float64
			}{
				{name: "total", got: total, want: tt.wantTot},
				{name: "usr", got: usr, want: tt.wantUsr},
				{name: "sys", got: sys, want: tt.wantSys},
			} {
				if math.Abs(c.got-c.want) > 1e-9 {
					t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
				}
			}
		})
	}
}

func TestCPUUtilCollectorFirstSamplePublishesNoUtilization(t *testing.T) {
	// A container that has been busy since it started: the cgroup counters are
	// lifetime totals, so they are already large on the very first scrape.
	const lifetimeUsage = uint64(time.Hour / time.Microsecond)

	usage := &cpuUsageCgroup{
		usage: stats.CpuUsage{
			Usage:  lifetimeUsage,
			User:   lifetimeUsage / 2,
			System: lifetimeUsage / 2,
		},
	}
	collector := cpuUtilCollector{cgroup: usage, numCores: 1}

	// First scrape: there is no previous sample, so no rate can be derived. The
	// zero lastTimestamp used to slip past the interval guard, because
	// now.Sub(time.Time{}) saturates at roughly 292 years, and the lifetime total
	// was then divided by that interval, publishing a total of a few 1e-5 percent.
	data, err := collector.updateHostDataCache()
	if err != nil {
		t.Fatalf("updateHostDataCache() error = %v", err)
	}
	if len(data) != 0 {
		t.Fatalf("first sample published %d series, want 0", len(data))
	}
	if collector.cpuDataCache.lastUsage.Usage != lifetimeUsage {
		t.Fatalf("baseline usage = %d, want %d", collector.cpuDataCache.lastUsage.Usage, lifetimeUsage)
	}
	if collector.cpuDataCache.lastTimestamp.IsZero() {
		t.Fatal("first sample did not record a baseline timestamp")
	}

	// Second scrape, one second of wall clock later, with one core saturated.
	const oneSecond = uint64(time.Second / time.Microsecond)
	usage.usage = stats.CpuUsage{
		Usage:  lifetimeUsage + oneSecond,
		User:   lifetimeUsage/2 + oneSecond/2,
		System: lifetimeUsage/2 + oneSecond/2,
	}
	collector.cpuDataCache.lastTimestamp = time.Now().Add(-time.Second)

	data, err = collector.updateHostDataCache()
	if err != nil {
		t.Fatalf("updateHostDataCache() error = %v", err)
	}
	if len(data) != 3 {
		t.Fatalf("second sample published %d series, want 3", len(data))
	}
	if !collector.cpuDataCache.utilValid {
		t.Fatal("second sample did not mark the utilization valid")
	}
	if got := collector.cpuDataCache.totalUtil; got < 90 || got > 100.01 {
		t.Fatalf("total utilization = %v, want about 100", got)
	}
}

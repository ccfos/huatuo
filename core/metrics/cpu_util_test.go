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
	"errors"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/cgroups/stats"
)

type cpuUsageCgroup struct {
	cgroups.Cgroup
	usage stats.CpuUsage
	err   error
}

func (c *cpuUsageCgroup) CpuUsage(string) (*stats.CpuUsage, error) {
	return &c.usage, c.err
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

func TestCPUUtilCollectorUpdateHostReadError(t *testing.T) {
	readErr := errors.New("host CPU usage unavailable")
	collector := &cpuUtilCollector{cgroup: &cpuUsageCgroup{err: readErr}, numCores: 1}
	metrics, err := collector.Update()
	if !errors.Is(err, readErr) {
		t.Fatalf("Update error = %v, want %v", err, readErr)
	}
	if len(metrics) != 0 {
		t.Fatalf("failed host read emitted %d metrics", len(metrics))
	}
	collector.cgroup = &cpuUsageCgroup{usage: stats.CpuUsage{Usage: 100, User: 60, System: 40}}
	metrics, err = collector.Update()
	if err != nil || len(metrics) != 3 {
		t.Fatalf("recovered host read = (%d metrics, %v), want 3 and nil", len(metrics), err)
	}
}

func BenchmarkCPUUtilCollectorUpdate(b *testing.B) {
	collector := &cpuUtilCollector{cgroup: &cpuUsageCgroup{}, numCores: 1}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := collector.Update(); err != nil {
			b.Fatal(err)
		}
	}
}

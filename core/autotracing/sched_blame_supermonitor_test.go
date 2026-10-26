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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeSchedBlameSuperMonitorCounters(
	t *testing.T,
	path string,
	counters schedBlameSuperMonitorCounters,
) {
	t.Helper()
	stat := fmt.Sprintf(
		"hierarchy_wait_sum %d\ninner_wait_sum %d\nthrottle_wait_sum %d\n",
		counters.hierarchyWait,
		counters.innerWait,
		counters.throttleWait,
	)
	require.NoError(t, os.WriteFile(
		filepath.Join(path, schedBlameSuperMonitorCPUStatFile),
		[]byte(stat),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(path, schedBlameSuperMonitorCPUUsageFile),
		[]byte(fmt.Sprintf("%d\n", counters.cpuUsage)),
		0o600,
	))
}

func TestSchedBlameSuperMonitorSampler(t *testing.T) {
	const (
		containerID = "0123456789abcdef"
		cgroupPath  = "/kubepods/container"
	)
	root := t.TempDir()
	path := filepath.Join(root, "cpu,cpuacct", "kubepods", "container")
	require.NoError(t, os.MkdirAll(path, 0o700))
	sampler := &schedBlameSuperMonitorSampler{
		root:      root,
		baselines: make(map[string]schedBlameSuperMonitorBaseline),
	}

	writeSchedBlameSuperMonitorCounters(t, path, schedBlameSuperMonitorCounters{
		hierarchyWait: 100,
		innerWait:     20,
		throttleWait:  10,
		cpuUsage:      1_000,
	})
	baseline := sampler.sample(containerID, cgroupPath, 1)
	assert.False(t, baseline.valid)
	assert.Equal(t, "baseline", baseline.invalidReason)

	writeSchedBlameSuperMonitorCounters(t, path, schedBlameSuperMonitorCounters{
		hierarchyWait: 200,
		innerWait:     40,
		throttleWait:  20,
		cpuUsage:      1_060,
	})
	sample := sampler.sample(containerID, cgroupPath, 1)
	require.True(t, sample.valid)
	assert.Equal(t, uint64(100), sample.hierarchyWaitDelta)
	assert.Equal(t, uint64(20), sample.innerWaitDelta)
	assert.Equal(t, uint64(10), sample.throttleWaitDelta)
	assert.Equal(t, uint64(70), sample.externalWaitDelta)
	assert.Equal(t, uint64(60), sample.cpuUsageDelta)
	assert.Equal(t, uint64(160), sample.totalDemandDelta)
	assert.InDelta(t, 0.4375, sample.waitrate, 1e-9)
}

func TestSchedBlameSuperMonitorSamplerResetsAfterCounterDecrease(t *testing.T) {
	const (
		containerID = "0123456789abcdef"
		cgroupPath  = "/kubepods/container"
	)
	root := t.TempDir()
	path := filepath.Join(root, "cpu", "kubepods", "container")
	require.NoError(t, os.MkdirAll(path, 0o700))
	sampler := &schedBlameSuperMonitorSampler{
		root:      root,
		baselines: make(map[string]schedBlameSuperMonitorBaseline),
	}

	writeSchedBlameSuperMonitorCounters(t, path, schedBlameSuperMonitorCounters{
		hierarchyWait: 100,
		cpuUsage:      100,
	})
	_ = sampler.sample(containerID, cgroupPath, 1)
	writeSchedBlameSuperMonitorCounters(t, path, schedBlameSuperMonitorCounters{
		hierarchyWait: 90,
		cpuUsage:      110,
	})
	sample := sampler.sample(containerID, cgroupPath, 1)
	assert.False(t, sample.valid)
	assert.Equal(t, "hierarchy_wait_decreased", sample.invalidReason)

	writeSchedBlameSuperMonitorCounters(t, path, schedBlameSuperMonitorCounters{
		hierarchyWait: 100,
		cpuUsage:      120,
	})
	sample = sampler.sample(containerID, cgroupPath, 1)
	require.True(t, sample.valid)
	assert.InDelta(t, 0.5, sample.waitrate, 1e-9)
}

func TestSchedBlameSuperMonitorSamplerResetsAfterCgidChange(t *testing.T) {
	const (
		containerID = "0123456789abcdef"
		cgroupPath  = "/kubepods/container"
	)
	root := t.TempDir()
	path := filepath.Join(root, "cpu,cpuacct", "kubepods", "container")
	require.NoError(t, os.MkdirAll(path, 0o700))
	sampler := &schedBlameSuperMonitorSampler{
		root:      root,
		baselines: make(map[string]schedBlameSuperMonitorBaseline),
	}

	writeSchedBlameSuperMonitorCounters(t, path, schedBlameSuperMonitorCounters{
		hierarchyWait: 100,
		cpuUsage:      100,
	})
	_ = sampler.sample(containerID, cgroupPath, 1)
	writeSchedBlameSuperMonitorCounters(t, path, schedBlameSuperMonitorCounters{
		hierarchyWait: 200,
		cpuUsage:      200,
	})

	sample := sampler.sample(containerID, cgroupPath, 2)
	assert.False(t, sample.valid)
	assert.Equal(t, "baseline", sample.invalidReason)
}

func TestCalculateSchedBlameSuperMonitorSampleRejectsInvalidDemand(t *testing.T) {
	inconsistent := calculateSchedBlameSuperMonitorSample(
		schedBlameSuperMonitorCounters{},
		schedBlameSuperMonitorCounters{
			hierarchyWait: 20,
			innerWait:     15,
			throttleWait:  10,
		},
	)
	assert.False(t, inconsistent.valid)
	assert.Equal(t,
		"hierarchy_less_than_inner_plus_throttle",
		inconsistent.invalidReason)

	empty := calculateSchedBlameSuperMonitorSample(
		schedBlameSuperMonitorCounters{},
		schedBlameSuperMonitorCounters{},
	)
	assert.False(t, empty.valid)
	assert.Equal(t, "no_runnable_demand", empty.invalidReason)
}

func TestSchedBlameSuperMonitorLogFields(t *testing.T) {
	assert.Equal(t, "supermonitor_waitrate_percent=N/A",
		schedBlameSuperMonitorLogFields(&schedBlameSuperMonitorSample{}))
	assert.Equal(t, "supermonitor_waitrate_percent=N/A",
		schedBlameSuperMonitorLogFields(&schedBlameSuperMonitorSample{
			invalidReason: "baseline",
		}))
	assert.Contains(t,
		schedBlameSuperMonitorLogFields(&schedBlameSuperMonitorSample{
			valid:    true,
			waitrate: 0.4375,
		}),
		"supermonitor_waitrate_percent=43.7500%")
}

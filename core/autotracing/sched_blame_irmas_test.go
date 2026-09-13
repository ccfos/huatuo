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

func writeSchedBlameIrmasCounters(
	t *testing.T,
	path string,
	counters schedBlameIrmasCounters,
) {
	t.Helper()
	stat := fmt.Sprintf(
		"hierarchy_wait_sum %d\ninner_wait_sum %d\nthrottle_wait_sum %d\n",
		counters.hierarchyWait,
		counters.innerWait,
		counters.throttleWait,
	)
	require.NoError(t, os.WriteFile(
		filepath.Join(path, schedBlameIrmasCPUStatFile),
		[]byte(stat),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(path, schedBlameIrmasCPUUsageFile),
		[]byte(fmt.Sprintf("%d\n", counters.cpuUsage)),
		0o600,
	))
}

func TestSchedBlameIrmasSampler(t *testing.T) {
	const (
		containerID = "0123456789abcdef"
		cgroupPath  = "/kubepods/container"
	)
	root := t.TempDir()
	path := filepath.Join(root, "cpu,cpuacct", "kubepods", "container")
	require.NoError(t, os.MkdirAll(path, 0o700))
	sampler := &schedBlameIrmasSampler{
		root:      root,
		baselines: make(map[string]schedBlameIrmasBaseline),
	}

	writeSchedBlameIrmasCounters(t, path, schedBlameIrmasCounters{
		hierarchyWait: 100,
		innerWait:     20,
		throttleWait:  10,
		cpuUsage:      1_000,
	})
	baseline := sampler.sample(containerID, cgroupPath, 1)
	assert.False(t, baseline.valid)
	assert.Equal(t, "baseline", baseline.invalidReason)

	writeSchedBlameIrmasCounters(t, path, schedBlameIrmasCounters{
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

func TestSchedBlameIrmasSamplerResetsAfterCounterDecrease(t *testing.T) {
	const (
		containerID = "0123456789abcdef"
		cgroupPath  = "/kubepods/container"
	)
	root := t.TempDir()
	path := filepath.Join(root, "cpu", "kubepods", "container")
	require.NoError(t, os.MkdirAll(path, 0o700))
	sampler := &schedBlameIrmasSampler{
		root:      root,
		baselines: make(map[string]schedBlameIrmasBaseline),
	}

	writeSchedBlameIrmasCounters(t, path, schedBlameIrmasCounters{
		hierarchyWait: 100,
		cpuUsage:      100,
	})
	_ = sampler.sample(containerID, cgroupPath, 1)
	writeSchedBlameIrmasCounters(t, path, schedBlameIrmasCounters{
		hierarchyWait: 90,
		cpuUsage:      110,
	})
	sample := sampler.sample(containerID, cgroupPath, 1)
	assert.False(t, sample.valid)
	assert.Equal(t, "hierarchy_wait_decreased", sample.invalidReason)

	writeSchedBlameIrmasCounters(t, path, schedBlameIrmasCounters{
		hierarchyWait: 100,
		cpuUsage:      120,
	})
	sample = sampler.sample(containerID, cgroupPath, 1)
	require.True(t, sample.valid)
	assert.InDelta(t, 0.5, sample.waitrate, 1e-9)
}

func TestSchedBlameIrmasSamplerResetsAfterCgidChange(t *testing.T) {
	const (
		containerID = "0123456789abcdef"
		cgroupPath  = "/kubepods/container"
	)
	root := t.TempDir()
	path := filepath.Join(root, "cpu,cpuacct", "kubepods", "container")
	require.NoError(t, os.MkdirAll(path, 0o700))
	sampler := &schedBlameIrmasSampler{
		root:      root,
		baselines: make(map[string]schedBlameIrmasBaseline),
	}

	writeSchedBlameIrmasCounters(t, path, schedBlameIrmasCounters{
		hierarchyWait: 100,
		cpuUsage:      100,
	})
	_ = sampler.sample(containerID, cgroupPath, 1)
	writeSchedBlameIrmasCounters(t, path, schedBlameIrmasCounters{
		hierarchyWait: 200,
		cpuUsage:      200,
	})

	sample := sampler.sample(containerID, cgroupPath, 2)
	assert.False(t, sample.valid)
	assert.Equal(t, "baseline", sample.invalidReason)
}

func TestCalculateSchedBlameIrmasSampleRejectsInvalidDemand(t *testing.T) {
	inconsistent := calculateSchedBlameIrmasSample(
		schedBlameIrmasCounters{},
		schedBlameIrmasCounters{
			hierarchyWait: 20,
			innerWait:     15,
			throttleWait:  10,
		},
	)
	assert.False(t, inconsistent.valid)
	assert.Equal(t,
		"hierarchy_less_than_inner_plus_throttle",
		inconsistent.invalidReason)

	empty := calculateSchedBlameIrmasSample(
		schedBlameIrmasCounters{},
		schedBlameIrmasCounters{},
	)
	assert.False(t, empty.valid)
	assert.Equal(t, "no_runnable_demand", empty.invalidReason)
}

func TestSchedBlameIrmasLogFields(t *testing.T) {
	assert.Equal(t, "irmas_waitrate_percent=N/A",
		schedBlameIrmasLogFields(&schedBlameIrmasSample{}))
	assert.Equal(t, "irmas_waitrate_percent=N/A",
		schedBlameIrmasLogFields(&schedBlameIrmasSample{
			invalidReason: "baseline",
		}))
	assert.Contains(t,
		schedBlameIrmasLogFields(&schedBlameIrmasSample{
			valid:    true,
			waitrate: 0.4375,
		}),
		"irmas_waitrate_percent=43.7500%")
}

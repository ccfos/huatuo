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
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"huatuo-bamai/internal/cgroups"
	"huatuo-bamai/internal/cgroups/stats"
	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/metric"
)

type cpuUtilCgroupStub struct {
	cgroups.Cgroup
	capacity      stats.CpuCapacity
	usage         stats.CpuUsage
	hostUsage     stats.CpuUsage
	capacityErr   error
	usageErr      error
	hostErr       error
	capacityRead  func() (*stats.CpuCapacity, error)
	duringUsage   func()
	capacityCalls int
	usageCalls    int
	onlineLists   []string
}

func (c *cpuUtilCgroupStub) CpuCapacity(path, onlineCPUs string) (*stats.CpuCapacity, error) {
	if path == "" {
		panic("host utilization must not read a cgroup quota")
	}
	c.capacityCalls++
	c.onlineLists = append(c.onlineLists, onlineCPUs)
	if c.capacityRead != nil {
		return c.capacityRead()
	}
	capacity := c.capacity
	return &capacity, c.capacityErr
}

func (c *cpuUtilCgroupStub) CpuUsage(path string) (*stats.CpuUsage, error) {
	c.usageCalls++
	if path == "" {
		usage := c.hostUsage
		return &usage, c.hostErr
	}
	usage := c.usage
	if c.duringUsage != nil {
		c.duringUsage()
	}
	return &usage, c.usageErr
}

func cpuUtilCapacity(cores float64, version byte) stats.CpuCapacity {
	return stats.CpuCapacity{Cores: cores, ConfigID: [32]byte{version}}
}

func cpuUtilUsage(total uint64) stats.CpuUsage {
	return stats.CpuUsage{Usage: total, User: total / 2, System: total / 4}
}

func cpuUtilContainer() *pod.Container {
	return &pod.Container{
		ID: "0123456789ab", CgroupPath: "/pod/container",
		Name: "worker", Hostname: "test-pod", Type: pod.ContainerTypeNormal,
		Labels: map[string]any{"HostNamespace": "test"},
	}
}

func assertCPUUtilMetrics(t *testing.T, got []*metric.Data, want map[string]float64) {
	t.Helper()
	require.Len(t, got, len(want))
	prefix := ""
	if _, container := want["cores"]; container {
		prefix = "container_"
	}
	expectedNames := make(map[string]float64, len(want))
	for name, value := range want {
		expectedNames[prefix+name] = value
	}
	for _, data := range got {
		expected, ok := expectedNames[data.Name()]
		require.True(t, ok, "unexpected metric %s", data.Name())
		assert.InDelta(t, expected, data.Value, 1e-9, "metric %s", data.Name())
		delete(expectedNames, data.Name())
	}
	assert.Empty(t, expectedNames)
}

func TestCPUUtilCollectorResizeAndBurst(t *testing.T) {
	start := time.Unix(100, 0)
	now := start
	stub := &cpuUtilCgroupStub{}
	collector := &cpuUtilCollector{cgroup: stub, now: func() time.Time { return now }}
	var cache cpuUtilStat
	container := cpuUtilContainer()
	steps := []struct {
		name    string
		second  int
		cores   float64
		version byte
		total   uint64
		want    map[string]float64
	}{
		{"first sample", 0, 2, 1, 10_000_000, map[string]float64{"cores": 2}},
		{"stable two cores", 1, 2, 1, 12_000_000, map[string]float64{"cores": 2, "total": 100, "usr": 50, "sys": 25}},
		{"resize to four", 2, 4, 2, 15_000_000, map[string]float64{"cores": 4}},
		{"stable four cores", 3, 4, 2, 17_000_000, map[string]float64{"cores": 4, "total": 50, "usr": 25, "sys": 12.5}},
		{"resize to one", 4, 1, 3, 19_000_000, map[string]float64{"cores": 1}},
		{"stable one core", 5, 1, 3, 19_500_000, map[string]float64{"cores": 1, "total": 50, "usr": 25, "sys": 12.5}},
		{"quota burst", 6, 1, 3, 20_750_000, map[string]float64{"cores": 1, "total": 125, "usr": 62.5, "sys": 31.25}},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			now = start.Add(time.Duration(step.second) * time.Second)
			stub.capacity = cpuUtilCapacity(step.cores, step.version)
			stub.usage = cpuUtilUsage(step.total)
			data, err := collector.updateContainerDataCache(&cache, container, "0-7")
			require.NoError(t, err)
			assertCPUUtilMetrics(t, data, step.want)
		})
	}
	require.Equal(t, len(steps)*2, stub.capacityCalls)
	for _, online := range stub.onlineLists {
		assert.Equal(t, "0-7", online)
	}
}

func TestCPUUtilCollectorConfigurationChangeAtSameCapacity(t *testing.T) {
	for _, name := range []string{"quota period", "cpuset membership", "ancestor limit", "cgroup identity"} {
		t.Run(name, func(t *testing.T) {
			now := time.Unix(100, 0)
			stub := &cpuUtilCgroupStub{capacity: cpuUtilCapacity(2, 1)}
			collector := &cpuUtilCollector{cgroup: stub, now: func() time.Time { return now }}
			var cache cpuUtilStat
			container := cpuUtilContainer()
			_, err := collector.updateContainerDataCache(&cache, container, "0-7")
			require.NoError(t, err)
			now = now.Add(time.Second)
			stub.usage = cpuUtilUsage(1_000_000)
			data, err := collector.updateContainerDataCache(&cache, container, "0-7")
			require.NoError(t, err)
			require.Len(t, data, 4)

			now = now.Add(time.Second)
			stub.capacity.ConfigID[0]++
			stub.usage = cpuUtilUsage(3_000_000)
			data, err = collector.updateContainerDataCache(&cache, container, "0-7")
			require.NoError(t, err)
			assertCPUUtilMetrics(t, data, map[string]float64{"cores": 2})

			now = now.Add(time.Second)
			stub.usage = cpuUtilUsage(4_000_000)
			data, err = collector.updateContainerDataCache(&cache, container, "0-7")
			require.NoError(t, err)
			assertCPUUtilMetrics(t, data, map[string]float64{"cores": 2, "total": 50, "usr": 25, "sys": 12.5})
		})
	}
}

func TestCPUUtilCollectorResizeDuringUsageRead(t *testing.T) {
	now := time.Unix(100, 0)
	stub := &cpuUtilCgroupStub{capacity: cpuUtilCapacity(2, 1)}
	collector := &cpuUtilCollector{cgroup: stub, now: func() time.Time { return now }}
	var cache cpuUtilStat
	container := cpuUtilContainer()
	_, err := collector.updateContainerDataCache(&cache, container, "0-7")
	require.NoError(t, err)
	now = now.Add(time.Second)
	stub.usage = cpuUtilUsage(2_000_000)
	stub.duringUsage = func() { stub.capacity = cpuUtilCapacity(4, 2) }
	data, err := collector.updateContainerDataCache(&cache, container, "0-7")
	require.NoError(t, err)
	assertCPUUtilMetrics(t, data, map[string]float64{"cores": 4})
	assert.True(t, cache.lastTimestamp.IsZero(), "mixed sample must not become a baseline")

	stub.duringUsage = nil
	now = now.Add(time.Second)
	stub.usage = cpuUtilUsage(5_000_000)
	data, err = collector.updateContainerDataCache(&cache, container, "0-7")
	require.NoError(t, err)
	assertCPUUtilMetrics(t, data, map[string]float64{"cores": 4})
	now = now.Add(time.Second)
	stub.usage = cpuUtilUsage(7_000_000)
	data, err = collector.updateContainerDataCache(&cache, container, "0-7")
	require.NoError(t, err)
	assertCPUUtilMetrics(t, data, map[string]float64{"cores": 4, "total": 50, "usr": 25, "sys": 12.5})
}

func TestCPUUtilCollectorErrorsInvalidateBaseline(t *testing.T) {
	failure := errors.New("cgroup read failed")
	for _, source := range []string{"capacity before", "usage", "capacity after"} {
		t.Run(source, func(t *testing.T) {
			now := time.Unix(100, 0)
			stub := &cpuUtilCgroupStub{capacity: cpuUtilCapacity(2, 1)}
			collector := &cpuUtilCollector{cgroup: stub, now: func() time.Time { return now }}
			var cache cpuUtilStat
			container := cpuUtilContainer()
			_, err := collector.updateContainerDataCache(&cache, container, "0-7")
			require.NoError(t, err)
			now = now.Add(time.Second)
			stub.usage = cpuUtilUsage(1_000_000)
			_, err = collector.updateContainerDataCache(&cache, container, "0-7")
			require.NoError(t, err)

			now = now.Add(time.Second)
			switch source {
			case "capacity before":
				stub.capacityErr = failure
			case "usage":
				stub.usageErr = failure
			case "capacity after":
				stub.duringUsage = func() { stub.capacityErr = failure }
			}
			data, err := collector.updateContainerDataCache(&cache, container, "0-7")
			require.ErrorIs(t, err, failure)
			if source == "capacity before" {
				require.Empty(t, data)
			} else {
				assertCPUUtilMetrics(t, data, map[string]float64{"cores": 2})
			}
			assert.True(t, cache.lastTimestamp.IsZero())

			stub.capacityErr, stub.usageErr, stub.duringUsage = nil, nil, nil
			now = now.Add(10 * time.Second)
			stub.usage = cpuUtilUsage(30_000_000)
			data, err = collector.updateContainerDataCache(&cache, container, "0-7")
			require.NoError(t, err)
			assertCPUUtilMetrics(t, data, map[string]float64{"cores": 2})
			now = now.Add(time.Second)
			stub.usage = cpuUtilUsage(31_000_000)
			data, err = collector.updateContainerDataCache(&cache, container, "0-7")
			require.NoError(t, err)
			assertCPUUtilMetrics(t, data, map[string]float64{"cores": 2, "total": 50, "usr": 25, "sys": 12.5})
		})
	}
}

func TestCPUUtilCollectorCapacitySnapshotIsCopied(t *testing.T) {
	now := time.Unix(100, 0)
	stub := &cpuUtilCgroupStub{capacity: cpuUtilCapacity(2, 1)}
	stub.capacityRead = func() (*stats.CpuCapacity, error) { return &stub.capacity, nil }
	collector := &cpuUtilCollector{cgroup: stub, now: func() time.Time { return now }}
	var cache cpuUtilStat
	container := cpuUtilContainer()
	_, err := collector.updateContainerDataCache(&cache, container, "0-7")
	require.NoError(t, err)
	now = now.Add(time.Second)
	stub.usage = cpuUtilUsage(1_000_000)
	stub.duringUsage = func() { stub.capacity.ConfigID[0]++ }
	data, err := collector.updateContainerDataCache(&cache, container, "0-7")
	require.NoError(t, err)
	assertCPUUtilMetrics(t, data, map[string]float64{"cores": 2})
	assert.True(t, cache.lastTimestamp.IsZero(), "same cores with different configuration is still a mixed sample")
}

func TestCPUUtilCollectorShortIntervalsDoNotReuseUtilization(t *testing.T) {
	start := time.Unix(100, 0)
	now := start
	stub := &cpuUtilCgroupStub{capacity: cpuUtilCapacity(2, 1)}
	collector := &cpuUtilCollector{cgroup: stub, now: func() time.Time { return now }}
	var cache cpuUtilStat
	container := cpuUtilContainer()
	for _, step := range []struct {
		elapsed  time.Duration
		capacity stats.CpuCapacity
		total    uint64
		want     map[string]float64
	}{
		{0, cpuUtilCapacity(2, 1), 0, map[string]float64{"cores": 2}},
		{100 * time.Millisecond, cpuUtilCapacity(4, 2), 100_000, map[string]float64{"cores": 4}},
		{200 * time.Millisecond, cpuUtilCapacity(1, 3), 200_000, map[string]float64{"cores": 1}},
		{1100 * time.Millisecond, cpuUtilCapacity(1, 3), 650_000, map[string]float64{"cores": 1}},
		{1200 * time.Millisecond, cpuUtilCapacity(1, 3), 700_000, map[string]float64{"cores": 1, "total": 50, "usr": 25, "sys": 12.5}},
		{1200 * time.Millisecond, cpuUtilCapacity(1, 3), 700_000, map[string]float64{"cores": 1}},
		{1201 * time.Millisecond, cpuUtilCapacity(1, 3), 700_500, map[string]float64{"cores": 1}},
	} {
		now = start.Add(step.elapsed)
		stub.capacity, stub.usage = step.capacity, cpuUtilUsage(step.total)
		data, err := collector.updateContainerDataCache(&cache, container, "0-7")
		require.NoError(t, err)
		assertCPUUtilMetrics(t, data, step.want)
	}
}

func TestCPUUtilCollectorCounterRegression(t *testing.T) {
	for _, field := range []string{"total", "user", "system"} {
		t.Run(field, func(t *testing.T) {
			now := time.Unix(100, 0)
			previous := stats.CpuUsage{Usage: 10_000_000, User: 6_000_000, System: 4_000_000}
			current := previous
			switch field {
			case "total":
				current.Usage--
			case "user":
				current.User--
			case "system":
				current.System--
			}
			stub := &cpuUtilCgroupStub{capacity: cpuUtilCapacity(2, 1), usage: previous}
			collector := &cpuUtilCollector{cgroup: stub, now: func() time.Time { return now }}
			var cache cpuUtilStat
			container := cpuUtilContainer()
			_, err := collector.updateContainerDataCache(&cache, container, "0-7")
			require.NoError(t, err)
			now = now.Add(time.Second)
			stub.usage = current
			data, err := collector.updateContainerDataCache(&cache, container, "0-7")
			require.NoError(t, err)
			assertCPUUtilMetrics(t, data, map[string]float64{"cores": 2})
			assert.Equal(t, current, cache.lastUsage)
			assert.Equal(t, now, cache.lastTimestamp)

			now = now.Add(time.Second)
			stub.usage = stats.CpuUsage{Usage: current.Usage + 1_000_000, User: current.User + 500_000, System: current.System + 250_000}
			data, err = collector.updateContainerDataCache(&cache, container, "0-7")
			require.NoError(t, err)
			assertCPUUtilMetrics(t, data, map[string]float64{"cores": 2, "total": 50, "usr": 25, "sys": 12.5})
		})
	}
}

func TestCPUUtilCollectorHostAndContainerIsolation(t *testing.T) {
	now := time.Unix(100, 0)
	stub := &cpuUtilCgroupStub{capacity: cpuUtilCapacity(2, 1)}
	collector := &cpuUtilCollector{cgroup: stub, now: func() time.Time { return now }}
	hostCapacity, err := hostCPUCapacity("0-7")
	require.NoError(t, err)
	var cache cpuUtilStat
	container := cpuUtilContainer()
	_, err = collector.updateContainerDataCache(&cache, container, "0-7")
	require.NoError(t, err)
	data, err := collector.updateHostDataCache(hostCapacity)
	require.NoError(t, err)
	require.Empty(t, data)

	now = now.Add(time.Second)
	stub.capacity = cpuUtilCapacity(4, 2)
	stub.usage, stub.hostUsage = cpuUtilUsage(2_000_000), cpuUtilUsage(4_000_000)
	data, err = collector.updateContainerDataCache(&cache, container, "0-7")
	require.NoError(t, err)
	assertCPUUtilMetrics(t, data, map[string]float64{"cores": 4})
	data, err = collector.updateHostDataCache(hostCapacity)
	require.NoError(t, err)
	assertCPUUtilMetrics(t, data, map[string]float64{"total": 50, "usr": 25, "sys": 12.5})

	now = now.Add(time.Second)
	stub.capacityErr = errors.New("container disappeared")
	stub.hostUsage = cpuUtilUsage(8_000_000)
	_, err = collector.updateContainerDataCache(&cache, container, "0-7")
	require.Error(t, err)
	data, err = collector.updateHostDataCache(hostCapacity)
	require.NoError(t, err)
	assertCPUUtilMetrics(t, data, map[string]float64{"total": 50, "usr": 25, "sys": 12.5})

	stub.capacityErr = nil
	_, err = collector.updateContainerDataCache(&cache, container, "0-7")
	require.NoError(t, err)
	now = now.Add(time.Second)
	stub.usage = cpuUtilUsage(4_000_000)
	stub.hostErr = errors.New("host read failed")
	data, err = collector.updateContainerDataCache(&cache, container, "0-7")
	require.NoError(t, err)
	assertCPUUtilMetrics(t, data, map[string]float64{"cores": 4, "total": 50, "usr": 25, "sys": 12.5})
	data, err = collector.updateHostDataCache(hostCapacity)
	require.Error(t, err)
	require.Empty(t, data)
	assert.True(t, collector.cpuDataCache.lastTimestamp.IsZero())

	stub.hostErr = nil
	now = now.Add(time.Second)
	stub.hostUsage = cpuUtilUsage(12_000_000)
	data, err = collector.updateHostDataCache(hostCapacity)
	require.NoError(t, err)
	require.Empty(t, data)
}

func TestCPUUtilCollectorOnlineCPUChanges(t *testing.T) {
	now := time.Unix(100, 0)
	online, reads := "0-1", 0
	var readErr error
	stub := &cpuUtilCgroupStub{}
	collector := &cpuUtilCollector{
		cgroup: stub, now: func() time.Time { return now },
		readOnlineCPUs: func() (string, error) { reads++; return online, readErr },
	}
	steps := []struct {
		online string
		total  uint64
		want   map[string]float64
	}{
		{"0-1", 0, nil},
		{"0-1", 1_000_000, map[string]float64{"total": 50, "usr": 25, "sys": 12.5}},
		{"0-3", 3_000_000, nil},
		{"0-3", 5_000_000, map[string]float64{"total": 50, "usr": 25, "sys": 12.5}},
		{"4-7", 7_000_000, nil},
		{"4-7", 9_000_000, map[string]float64{"total": 50, "usr": 25, "sys": 12.5}},
	}
	for _, step := range steps {
		online, stub.hostUsage = step.online, cpuUtilUsage(step.total)
		data, err := collector.updateMetrics(nil)
		require.NoError(t, err)
		assertCPUUtilMetrics(t, data, step.want)
		now = now.Add(time.Second)
	}
	assert.Equal(t, len(steps), reads, "online CPUs should be read once per update")

	readErr = errors.New("online CPUs unavailable")
	data, err := collector.updateMetrics(nil)
	require.ErrorIs(t, err, readErr)
	require.Empty(t, data)
	assert.True(t, collector.cpuDataCache.lastTimestamp.IsZero())
	readErr = nil
	now = now.Add(time.Second)
	stub.hostUsage = cpuUtilUsage(12_000_000)
	data, err = collector.updateMetrics(nil)
	require.NoError(t, err)
	require.Empty(t, data)
}

func TestCPUUtilCollectorInvalidCapacity(t *testing.T) {
	for _, cores := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		stub := &cpuUtilCgroupStub{capacity: cpuUtilCapacity(cores, 1)}
		collector := &cpuUtilCollector{cgroup: stub}
		cache := cpuUtilStat{lastTimestamp: time.Unix(100, 0)}
		data, err := collector.updateContainerDataCache(&cache, cpuUtilContainer(), "0-7")
		require.Error(t, err)
		require.Empty(t, data)
		assert.True(t, cache.lastTimestamp.IsZero())
	}
}

func TestCPUUtilCollectorInvalidOnlineCPUList(t *testing.T) {
	for _, online := range []string{"", "invalid", "3-1"} {
		t.Run(online, func(t *testing.T) {
			stub := &cpuUtilCgroupStub{}
			collector := &cpuUtilCollector{
				cgroup:         stub,
				cpuDataCache:   cpuUtilStat{lastTimestamp: time.Unix(100, 0)},
				readOnlineCPUs: func() (string, error) { return online, nil },
			}
			data, err := collector.updateMetrics(nil)
			require.Error(t, err)
			require.Empty(t, data)
			assert.Zero(t, stub.usageCalls)
			assert.True(t, collector.cpuDataCache.lastTimestamp.IsZero())
		})
	}
}

func TestCPUUtilStateClockRegressionAndLargeCounters(t *testing.T) {
	start := time.Unix(100, 0)
	capacity := cpuUtilCapacity(1, 1)
	cache := cpuUtilStat{lastTimestamp: start, capacity: capacity}
	_, available := cache.update(cpuUtilUsage(100), capacity, start.Add(-time.Second))
	require.False(t, available)
	assert.Equal(t, start.Add(-time.Second), cache.lastTimestamp)

	cache = cpuUtilStat{lastTimestamp: start, capacity: capacity}
	sample, available := cache.update(stats.CpuUsage{Usage: math.MaxUint64, User: math.MaxUint64, System: math.MaxUint64}, capacity, start.Add(time.Second))
	require.True(t, available)
	assert.Greater(t, sample.total, float64(100))
	assert.Equal(t, sample.total, sample.usr)
	assert.Equal(t, sample.total, sample.sys)
	assert.False(t, math.IsInf(sample.total, 0))
}

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
	"os"
	"path/filepath"
	"testing"

	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/internal/procfs/blockdevice"
	metricpkg "huatuo-bamai/pkg/metric"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testDiskstats = `   8       0 sda 1000 200 50000 3000 2000 400 80000 6000 50 9000 15000 0 0 0 0 100 500
   8       1 sda1 800 100 40000 2000 1500 300 60000 4000 30 6000 10000 0 0 0 0 80 300
`
	testStat = `cpu  10000 2000 5000 50000 3000 1000 500 200 0 0
cpu0 5000 1000 2500 25000 1500 500 250 100 0 0
cpu1 5000 1000 2500 25000 1500 500 250 100 0 0
intr 100000 50 60 70
ctxt 200000
btime 1700000000
processes 5000
procs_running 3
procs_blocked 1
softirq 50000 100 200 300 400 500 600 700 800 900 1000
`
	testStatWithIOWaitSpike = `cpu  10100 2000 5000 50000 3100 1000 500 200 0 0
cpu0 5000 1000 2500 25000 1500 500 250 100 0 0
cpu1 5000 1000 2500 25000 1500 500 250 100 0 0
intr 100000 50 60 70
ctxt 200000
btime 1700000000
processes 5000
procs_running 3
procs_blocked 1
softirq 50000 100 200 300 400 500 600 700 800 900 1000
`
	testStatWithInvalidIOWait = `cpu  8100 2000 5000 50000 5000 1000 500 200 0 0
cpu0 5000 1000 2500 25000 1500 500 250 100 0 0
cpu1 5000 1000 2500 25000 1500 500 250 100 0 0
intr 100000 50 60 70
ctxt 200000
btime 1700000000
processes 5000
procs_running 3
procs_blocked 1
softirq 50000 100 200 300 400 500 600 700 800 900 1000
`
)

func newTestCollector(t testing.TB, diskstats, stat string) (*diskIOStatsCollector, string) {
	t.Helper()

	tmpRoot := t.TempDir()
	procDir := filepath.Join(tmpRoot, "proc")
	require.NoError(t, os.MkdirAll(procDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(procDir, "diskstats"),
		[]byte(diskstats),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(procDir, "stat"),
		[]byte(stat),
		0o600,
	))

	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	procfs.RootPrefix(tmpRoot)

	devFS, err := blockdevice.NewDiskstatsFS()
	require.NoError(t, err)
	procFS, err := procfs.NewDefaultFS()
	require.NoError(t, err)

	return &diskIOStatsCollector{
		devFS:  devFS,
		procFS: procFS,
	}, procDir
}

func metricValues(metrics []*metricpkg.Data) []float64 {
	values := make([]float64, 0, len(metrics))
	for _, data := range metrics {
		values = append(values, data.Value)
	}
	return values
}

func countMetricValue(metrics []*metricpkg.Data, expected float64) int {
	var count int
	for _, data := range metrics {
		if data.Value == expected {
			count++
		}
	}
	return count
}

func TestDiskIOStatsCollector_Update(t *testing.T) {
	collector, procDir := newTestCollector(t, testDiskstats, testStat)

	metrics, err := collector.Update()
	require.NoError(t, err)
	require.Len(t, metrics, 14)

	require.NoError(t, os.WriteFile(
		filepath.Join(procDir, "stat"),
		[]byte(testStatWithIOWaitSpike),
		0o600,
	))

	metrics, err = collector.Update()
	require.NoError(t, err)
	require.Len(t, metrics, 15)
	assert.Equal(t, 2, countMetricValue(metrics, 50))
}

func TestNewDiskIO_UsesProcfsPrefixWithoutSysfs(t *testing.T) {
	tmpRoot := t.TempDir()
	procDir := filepath.Join(tmpRoot, "proc")
	require.NoError(t, os.MkdirAll(procDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(procDir, "diskstats"),
		[]byte(testDiskstats),
		0o600,
	))
	require.NoError(t, os.WriteFile(
		filepath.Join(procDir, "stat"),
		[]byte(testStat),
		0o600,
	))

	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	procfs.RootPrefix(tmpRoot)

	attr, err := newDiskIO()
	require.NoError(t, err)
	collector, ok := attr.TracingData.(*diskIOStatsCollector)
	require.True(t, ok)

	metrics, err := collector.Update()
	require.NoError(t, err)
	assert.Len(t, metrics, 14)
}

func TestDiskIOStatsCollector_CollectDiskstats(t *testing.T) {
	collector, _ := newTestCollector(t, testDiskstats, testStat)

	metrics, err := collector.collectDiskstats()
	require.NoError(t, err)
	require.Len(t, metrics, 14)

	assert.ElementsMatch(t, []float64{
		1000, 2000, 25_600_000, 40_960_000, 3, 6, 50,
		800, 1500, 20_480_000, 30_720_000, 2, 4, 30,
	}, metricValues(metrics))
}

func TestDiskIOStatsCollector_MetricContract(t *testing.T) {
	collector, procDir := newTestCollector(t, testDiskstats, testStat)

	metrics, err := collector.collectDiskstats()
	require.NoError(t, err)
	require.Len(t, metrics, 14)

	expected := []struct {
		name       string
		metricType int
	}{
		{name: "read_requests_total", metricType: metricpkg.MetricTypeCounter},
		{name: "write_requests_total", metricType: metricpkg.MetricTypeCounter},
		{name: "read_bytes_total", metricType: metricpkg.MetricTypeCounter},
		{name: "written_bytes_total", metricType: metricpkg.MetricTypeCounter},
		{name: "read_time_seconds_total", metricType: metricpkg.MetricTypeCounter},
		{name: "write_time_seconds_total", metricType: metricpkg.MetricTypeCounter},
		{name: "io_in_progress", metricType: metricpkg.MetricTypeGauge},
	}
	for i := range expected {
		assert.Equal(t, expected[i].name, metrics[i].Name())
		assert.Equal(
			t,
			"huatuo_bamai_diskio_"+expected[i].name,
			prometheus.BuildFQName(metricpkg.DefaultNamespace, "diskio", metrics[i].Name()),
		)
		assert.Equal(t, expected[i].metricType, metrics[i].Type())
		assert.Equal(t, "sda", metrics[i].Labels()["device"])
		assert.NotEmpty(t, metrics[i].Help())
	}

	iowaitMetrics, err := collector.collectIOWait()
	require.NoError(t, err)
	assert.Empty(t, iowaitMetrics)

	require.NoError(t, os.WriteFile(
		filepath.Join(procDir, "stat"),
		[]byte(testStatWithIOWaitSpike),
		0o600,
	))
	iowaitMetrics, err = collector.collectIOWait()
	require.NoError(t, err)
	require.Len(t, iowaitMetrics, 1)
	assert.Equal(t, "disk_iowait_percent", iowaitMetrics[0].Name())
	assert.Equal(t, metricpkg.MetricTypeGauge, iowaitMetrics[0].Type())
	assert.NotEmpty(t, iowaitMetrics[0].Help())
}

func TestDiskIOStatsCollector_CollectDiskstats_ZeroTicks(t *testing.T) {
	const diskstats = "8 0 sda 1000 0 50000 0 2000 0 80000 0 0 0 0\n"
	collector, _ := newTestCollector(t, diskstats, testStat)

	metrics, err := collector.collectDiskstats()
	require.NoError(t, err)
	require.Len(t, metrics, 7)

	assert.Equal(t, 3, countMetricValue(metrics, 0))
}

func TestDiskIOStatsCollector_CollectIOWait(t *testing.T) {
	collector, procDir := newTestCollector(t, "", testStat)

	metrics, err := collector.collectIOWait()
	require.NoError(t, err)
	assert.Empty(t, metrics)

	require.NoError(t, os.WriteFile(
		filepath.Join(procDir, "stat"),
		[]byte(testStatWithIOWaitSpike),
		0o600,
	))

	metrics, err = collector.collectIOWait()
	require.NoError(t, err)
	require.Len(t, metrics, 1)
	assert.InDelta(t, 50, metrics[0].Value, 0.001)
}

func TestDiskIOStatsCollector_CollectIOWait_CounterReset(t *testing.T) {
	collector, procDir := newTestCollector(t, "", testStatWithIOWaitSpike)

	metrics, err := collector.collectIOWait()
	require.NoError(t, err)
	assert.Empty(t, metrics)

	require.NoError(t, os.WriteFile(
		filepath.Join(procDir, "stat"),
		[]byte(testStat),
		0o600,
	))

	metrics, err = collector.collectIOWait()
	require.NoError(t, err)
	assert.Empty(t, metrics)
	assert.Equal(t, cpuIOWaitSample{iowait: 30, total: 717}, collector.prevCPU)
}

func TestDiskIOStatsCollector_CollectIOWait_InvalidDelta(t *testing.T) {
	collector, procDir := newTestCollector(t, "", testStat)

	metrics, err := collector.collectIOWait()
	require.NoError(t, err)
	assert.Empty(t, metrics)

	require.NoError(t, os.WriteFile(
		filepath.Join(procDir, "stat"),
		[]byte(testStatWithInvalidIOWait),
		0o600,
	))

	metrics, err = collector.collectIOWait()
	require.NoError(t, err)
	assert.Empty(t, metrics)
}

func TestDiskIOStatsCollector_Update_EmptyDiskstats(t *testing.T) {
	collector, procDir := newTestCollector(t, "", testStat)

	metrics, err := collector.Update()
	assert.ErrorIs(t, err, metricpkg.ErrNoData)
	assert.Empty(t, metrics)

	require.NoError(t, os.WriteFile(
		filepath.Join(procDir, "stat"),
		[]byte(testStatWithIOWaitSpike),
		0o600,
	))

	metrics, err = collector.Update()
	require.NoError(t, err)
	require.Len(t, metrics, 1)
	assert.InDelta(t, 50, metrics[0].Value, 0.001)
}

func TestDiskIOStatsCollector_Update_ReturnsIOWaitOnDiskstatsFailure(t *testing.T) {
	collector, procDir := newTestCollector(t, testDiskstats, testStat)

	_, err := collector.Update()
	require.NoError(t, err)

	require.NoError(t, os.Remove(filepath.Join(procDir, "diskstats")))
	require.NoError(t, os.WriteFile(
		filepath.Join(procDir, "stat"),
		[]byte(testStatWithIOWaitSpike),
		0o600,
	))

	metrics, err := collector.Update()
	require.Error(t, err)
	assert.ErrorContains(t, err, "collect diskstats")
	require.Len(t, metrics, 1)
	assert.InDelta(t, 50, metrics[0].Value, 0.001)
}

func TestDiskIOStatsCollector_Update_PropagatesErrors(t *testing.T) {
	tests := []struct {
		name            string
		remove          []string
		errContain      []string
		wantMetricCount int
	}{
		{
			name:       "diskstats failure",
			remove:     []string{"diskstats"},
			errContain: []string{"collect diskstats"},
		},
		{
			name:            "stat failure",
			remove:          []string{"stat"},
			errContain:      []string{"collect iowait"},
			wantMetricCount: 14,
		},
		{
			name:       "both failures",
			remove:     []string{"diskstats", "stat"},
			errContain: []string{"collect diskstats", "collect iowait"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector, procDir := newTestCollector(t, testDiskstats, testStat)
			for _, name := range tt.remove {
				require.NoError(t, os.Remove(filepath.Join(procDir, name)))
			}

			metrics, err := collector.Update()
			require.Error(t, err)
			assert.False(t, errors.Is(err, metricpkg.ErrNoData))
			assert.Len(t, metrics, tt.wantMetricCount)
			for _, text := range tt.errContain {
				assert.ErrorContains(t, err, text)
			}
		})
	}
}

func BenchmarkDiskIOStatsCollector_CollectDiskstats(b *testing.B) {
	collector, _ := newTestCollector(b, testDiskstats, testStat)

	b.ReportAllocs()
	for b.Loop() {
		if _, err := collector.collectDiskstats(); err != nil {
			b.Fatal(err)
		}
	}
}

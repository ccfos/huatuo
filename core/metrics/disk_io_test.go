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
	"os"
	"path/filepath"
	"testing"
	"time"

	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/internal/procfs/blockdevice"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockProcFS creates a temporary directory structure mimicking /proc with the
// given diskstats and stat file contents.
func mockProcFS(t *testing.T, diskstats, stat string) string {
	t.Helper()
	tmpDir := t.TempDir()

	procDir := filepath.Join(tmpDir, "proc")
	require.NoError(t, os.MkdirAll(procDir, 0o755))

	// sys dir is needed by blockdevice.FS
	sysDir := filepath.Join(tmpDir, "sys")
	require.NoError(t, os.MkdirAll(sysDir, 0o755))

	require.NoError(t, os.WriteFile(filepath.Join(procDir, "diskstats"), []byte(diskstats), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(procDir, "stat"), []byte(stat), 0o644))

	return tmpDir
}

const testDiskstats = `   8       0 sda 1000 200 50000 3000 2000 400 80000 6000 50 9000 15000 0 0 0 0 100 500
   8       1 sda1 800 100 40000 2000 1500 300 60000 4000 30 6000 10000 0 0 0 0 80 300
`

const testStat = `cpu  10000 2000 5000 50000 3000 1000 500 200 0 0
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

func newTestCollector(t *testing.T) *diskIOCollector {
	t.Helper()
	tmpRoot := mockProcFS(t, testDiskstats, testStat)

	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	procfs.RootPrefix(tmpRoot)

	devFS, err := blockdevice.NewDefaultFS()
	require.NoError(t, err)

	procFS, err := procfs.NewDefaultFS()
	require.NoError(t, err)

	return &diskIOCollector{
		prev:   make(map[string]*diskDeviceStats),
		devFS:  devFS,
		procFS: procFS,
	}
}

func TestDiskIOCollector_Update_FirstCall(t *testing.T) {
	c := newTestCollector(t)

	metrics, err := c.Update()
	require.NoError(t, err)
	assert.NotEmpty(t, metrics)

	// First call: 2 devices × 4 counters + 2 queue depth gauges + 1 iowait = 11
	// No latency gauges yet (no previous data for delta).
	assert.Equal(t, 11, len(metrics), "first call should return 11 metrics")

	// Verify previous data was cached for both devices.
	assert.Contains(t, c.prev, "sda")
	assert.Contains(t, c.prev, "sda1")
	assert.Equal(t, uint64(1000), c.prev["sda"].readIOs)
	assert.Equal(t, uint64(2000), c.prev["sda"].writeIOs)
}

func TestDiskIOCollector_Update_SecondCall(t *testing.T) {
	c := newTestCollector(t)

	// First call to populate previous values.
	_, err := c.Update()
	require.NoError(t, err)

	// Wait briefly so elapsed time > 0 for rate calculations.
	time.Sleep(10 * time.Millisecond)

	// Second call with the same mock data — all deltas are zero,
	// so no latency gauges are emitted.
	metrics, err := c.Update()
	require.NoError(t, err)
	assert.NotEmpty(t, metrics)

	// With identical data between calls, delta-based gauges are not produced.
	// 2 devices × (4 counters + 1 queue depth) + 1 iowait = 11
	assert.Equal(t, 11, len(metrics), "second call with same data should return same metric count")
}

func TestDiskIOCollector_CollectIOWait(t *testing.T) {
	tmpRoot := mockProcFS(t, "", testStat)
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	procfs.RootPrefix(tmpRoot)

	procFS, err := procfs.NewDefaultFS()
	require.NoError(t, err)

	c := &diskIOCollector{
		prev:   make(map[string]*diskDeviceStats),
		procFS: procFS,
	}

	metrics, err := c.collectIOWait()
	require.NoError(t, err)
	require.Len(t, metrics, 1)

	// iowait = 3000, total = 10000+2000+5000+50000+3000+1000+500+200 = 71700
	// ratio = 3000/71700 * 100 ≈ 4.184%
	assert.InDelta(t, 4.184, metrics[0].Value, 0.01)
}

func TestDiskIOCollector_CollectDiskstats_DeviceCount(t *testing.T) {
	c := newTestCollector(t)

	metrics, err := c.collectDiskstats()
	require.NoError(t, err)

	// 2 devices × (4 counters + 1 queue depth gauge) = 10
	assert.Equal(t, 10, len(metrics))
}

func TestDiskIOCollector_EmptyDiskstats(t *testing.T) {
	tmpRoot := mockProcFS(t, "", testStat)
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	procfs.RootPrefix(tmpRoot)

	devFS, err := blockdevice.NewDefaultFS()
	require.NoError(t, err)

	procFS, err := procfs.NewDefaultFS()
	require.NoError(t, err)

	c := &diskIOCollector{
		prev:   make(map[string]*diskDeviceStats),
		devFS:  devFS,
		procFS: procFS,
	}

	metrics, err := c.Update()
	require.NoError(t, err)

	// Should still have iowait metric even with empty diskstats.
	assert.NotEmpty(t, metrics)
	assert.Equal(t, 1, len(metrics), "only iowait metric expected")
}

func TestDiskIOCollector_PrevStatsTracking(t *testing.T) {
	c := newTestCollector(t)

	// First call.
	_, err := c.Update()
	require.NoError(t, err)

	sda := c.prev["sda"]
	require.NotNil(t, sda)
	assert.Equal(t, uint64(1000), sda.readIOs)
	assert.Equal(t, uint64(2000), sda.writeIOs)
	assert.Equal(t, uint64(3000), sda.readTicks)
	assert.Equal(t, uint64(6000), sda.writeTicks)
	assert.NotZero(t, sda.lastUpdate)
}

// TestDiskIOCollector_CounterReset verifies that when counters decrease
// (simulating a device reset or hot-unplug), latency gauges are skipped.
func TestDiskIOCollector_CounterReset(t *testing.T) {
	tmpDir := t.TempDir()
	procDir := filepath.Join(tmpDir, "proc")
	sysDir := filepath.Join(tmpDir, "sys")
	require.NoError(t, os.MkdirAll(procDir, 0o755))
	require.NoError(t, os.MkdirAll(sysDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(procDir, "stat"), []byte(testStat), 0o644))

	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	procfs.RootPrefix(tmpDir)

	// First diskstats: high values.
	require.NoError(t, os.WriteFile(filepath.Join(procDir, "diskstats"),
		[]byte("   8       0 sda 1000 200 50000 3000 2000 400 80000 6000 50 9000 15000\n"), 0o644))

	devFS, err := blockdevice.NewDefaultFS()
	require.NoError(t, err)
	procFS, err := procfs.NewDefaultFS()
	require.NoError(t, err)

	c := &diskIOCollector{
		prev:   make(map[string]*diskDeviceStats),
		devFS:  devFS,
		procFS: procFS,
	}

	// First call to populate prev.
	_, err = c.Update()
	require.NoError(t, err)

	// Second diskstats: counters DECREASED (simulating reset).
	require.NoError(t, os.WriteFile(filepath.Join(procDir, "diskstats"),
		[]byte("   8       0 sda 500 100 25000 1500 1000 200 40000 3000 25 4500 7500\n"), 0o644))

	// Re-create devFS to pick up new file.
	devFS, err = blockdevice.NewDefaultFS()
	require.NoError(t, err)
	c.devFS = devFS

	time.Sleep(10 * time.Millisecond)

	metrics, err := c.Update()
	require.NoError(t, err)

	// With counter reset, no latency gauges should be emitted.
	// 1 device × (4 counters + 1 queue depth) + 1 iowait = 6
	assert.Equal(t, 6, len(metrics), "counter reset should skip latency gauges")
}

// TestDiskIOCollector_LatencyComputation verifies that latency gauges are
// correctly computed when counters increase between calls.
func TestDiskIOCollector_LatencyComputation(t *testing.T) {
	tmpDir := t.TempDir()
	procDir := filepath.Join(tmpDir, "proc")
	sysDir := filepath.Join(tmpDir, "sys")
	require.NoError(t, os.MkdirAll(procDir, 0o755))
	require.NoError(t, os.MkdirAll(sysDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(procDir, "stat"), []byte(testStat), 0o644))

	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	procfs.RootPrefix(tmpDir)

	// First diskstats: readIOs=1000, readTicks=3000; writeIOs=2000, writeTicks=6000.
	require.NoError(t, os.WriteFile(filepath.Join(procDir, "diskstats"),
		[]byte("   8       0 sda 1000 200 50000 3000 2000 400 80000 6000 50 9000 15000\n"), 0o644))

	devFS, err := blockdevice.NewDefaultFS()
	require.NoError(t, err)
	procFS, err := procfs.NewDefaultFS()
	require.NoError(t, err)

	c := &diskIOCollector{
		prev:   make(map[string]*diskDeviceStats),
		devFS:  devFS,
		procFS: procFS,
	}

	// First call to populate prev.
	_, err = c.Update()
	require.NoError(t, err)

	// Second diskstats: readIOs increased by 100, readTicks by 500 → avg latency = 5ms.
	// writeIOs increased by 200, writeTicks by 1000 → avg latency = 5ms.
	require.NoError(t, os.WriteFile(filepath.Join(procDir, "diskstats"),
		[]byte("   8       0 sda 1100 200 55000 3500 2200 400 90000 7000 50 9000 15000\n"), 0o644))

	// Re-create devFS to pick up new file.
	devFS, err = blockdevice.NewDefaultFS()
	require.NoError(t, err)
	c.devFS = devFS

	time.Sleep(10 * time.Millisecond)

	metrics, err := c.Update()
	require.NoError(t, err)

	// Find latency gauges by checking values.
	// With 1 device and actual deltas, we should have:
	// 4 counters + 1 queue depth + 2 latency gauges + 1 iowait = 8
	assert.Equal(t, 8, len(metrics), "latency gauges should be emitted with actual deltas")

	// Verify latency values are correct.
	// deltaReadTicks=500 / deltaReadIOs=100 = 5.0 ms
	// deltaWriteTicks=1000 / deltaWriteIOs=200 = 5.0 ms
	// The latency values should be 5.0 ms each.
	var latencyValues []float64
	for _, m := range metrics {
		if m.Value == 5.0 {
			latencyValues = append(latencyValues, m.Value)
		}
	}
	assert.GreaterOrEqual(t, len(latencyValues), 2, "should have at least 2 latency values of 5.0ms")
}

// TestDiskIOCollector_ZeroTicks verifies that when ticks remain zero
// (e.g., old md devices on kernel < 5.14 that don't support IO accounting),
// no latency gauges are emitted even though IOs increase.
func TestDiskIOCollector_ZeroTicks(t *testing.T) {
	tmpDir := t.TempDir()
	procDir := filepath.Join(tmpDir, "proc")
	sysDir := filepath.Join(tmpDir, "sys")
	require.NoError(t, os.MkdirAll(procDir, 0o755))
	require.NoError(t, os.MkdirAll(sysDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(procDir, "stat"), []byte(testStat), 0o644))

	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	procfs.RootPrefix(tmpDir)

	// First diskstats: readIOs=1000, readTicks=0 (no IO accounting).
	require.NoError(t, os.WriteFile(filepath.Join(procDir, "diskstats"),
		[]byte("   8       0 sda 1000 200 50000 0 2000 400 80000 0 50 9000 15000\n"), 0o644))

	devFS, err := blockdevice.NewDefaultFS()
	require.NoError(t, err)
	procFS, err := procfs.NewDefaultFS()
	require.NoError(t, err)

	c := &diskIOCollector{
		prev:   make(map[string]*diskDeviceStats),
		devFS:  devFS,
		procFS: procFS,
	}

	// First call to populate prev.
	_, err = c.Update()
	require.NoError(t, err)

	// Second diskstats: IOs increased but ticks still 0.
	require.NoError(t, os.WriteFile(filepath.Join(procDir, "diskstats"),
		[]byte("   8       0 sda 1100 200 55000 0 2200 400 90000 0 50 9000 15000\n"), 0o644))

	// Re-create devFS to pick up new file.
	devFS, err = blockdevice.NewDefaultFS()
	require.NoError(t, err)
	c.devFS = devFS

	time.Sleep(10 * time.Millisecond)

	metrics, err := c.Update()
	require.NoError(t, err)

	// With zero ticks, no latency gauges should be emitted.
	// 1 device × (4 counters + 1 queue depth) + 1 iowait = 6
	assert.Equal(t, 6, len(metrics), "zero ticks should skip latency gauges")
}

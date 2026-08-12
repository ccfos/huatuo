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

// Tests cover IO disk windows, metric values, and diskstats collection.
package autotracing

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/procfs/blockdevice"
	"github.com/ccfos/huatuo/pkg/metric"

	promblockdevice "github.com/prometheus/procfs/blockdevice"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Disk window and exported metric contracts.

func metricTestSnapshot(
	at time.Time,
	device string,
	major,
	minor uint32,
	stats *promblockdevice.IOStats,
) *rawDiskstatsSnapshot {
	disk := blockdevice.Diskstats{
		Info: promblockdevice.Info{
			DeviceName:  device,
			MajorNumber: major,
			MinorNumber: minor,
		},
		IOStats: *stats,
	}
	return &rawDiskstatsSnapshot{
		timestamp: at,
		devices:   map[string]blockdevice.Diskstats{device: disk},
		order:     []string{device},
	}
}

func metricTestValues(metrics []*metric.Data) []float64 {
	values := make([]float64, 0, len(metrics))
	for _, data := range metrics {
		values = append(values, data.Value)
	}
	return values
}

func TestNewIOTracingInitializesMetricBaseline(t *testing.T) {
	previousConfig := configSnapshot()
	Set(validIOTracingConfig())
	t.Cleanup(func() { Set(previousConfig) })
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	root := t.TempDir()
	procfs.RootPrefix(root)
	t.Cleanup(func() {
		procfs.RootPrefix(originalPrefix)
	})

	require.NoError(t, os.MkdirAll(
		filepath.Join(root, "sys", "dev", "block", "8:0"),
		0o755,
	))
	require.NoError(t, os.MkdirAll(filepath.Join(root, "proc"), 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "proc", "diskstats"),
		[]byte("8 0 sda 100 0 1000 100 0 0 0 0 0 100 100\n"),
		0o600,
	))

	attr, err := newIOTracing()
	require.NoError(t, err)
	tracer, ok := attr.TracingData.(*ioTracing)
	require.True(t, ok)
	require.NotNil(t, tracer.metricPrevious)
	assert.Contains(t, tracer.metricPrevious.devices, "sda")
}

func TestIOTracingUpdateUsesConsecutiveMetricReads(t *testing.T) {
	startedAt := time.Unix(100, 0)
	previous := metricTestSnapshot(
		startedAt,
		"sda",
		8,
		0,
		&promblockdevice.IOStats{
			ReadIOs:         100,
			ReadSectors:     1000,
			ReadTicks:       1000,
			WriteIOs:        50,
			WriteSectors:    500,
			WriteTicks:      500,
			IOsTotalTicks:   100,
			WeightedIOTicks: 200,
		},
	)
	reads := []*rawDiskstatsSnapshot{
		metricTestSnapshot(
			startedAt.Add(2*time.Second),
			"sda",
			8,
			0,
			&promblockdevice.IOStats{
				ReadIOs:         103,
				ReadSectors:     1005,
				ReadTicks:       4751,
				WriteIOs:        55,
				WriteSectors:    507,
				WriteTicks:      13003,
				IOsTotalTicks:   2000,
				WeightedIOTicks: 446,
			},
		),
		metricTestSnapshot(
			startedAt.Add(3*time.Second),
			"sda",
			8,
			0,
			&promblockdevice.IOStats{
				ReadIOs:         107,
				ReadSectors:     1015,
				ReadTicks:       6751,
				WriteIOs:        57,
				WriteSectors:    511,
				WriteTicks:      14003,
				IOsTotalTicks:   2500,
				WeightedIOTicks: 646,
			},
		),
	}

	readIndex := 0
	tracer := &ioTracing{
		metricPrevious: previous,
		readMetricSnapshot: func() (*rawDiskstatsSnapshot, error) {
			if readIndex >= len(reads) {
				return nil, errors.New("unexpected metric diskstats read")
			}
			current := reads[readIndex]
			readIndex++
			return current, nil
		},
	}

	first, err := tracer.Update()
	require.NoError(t, err)
	assert.Equal(t, []float64{
		1280,
		1792,
		1.5,
		2.5,
		1250.33,
		2500.6,
		95,
		0.12,
	}, metricTestValues(first))

	second, err := tracer.Update()
	require.NoError(t, err)
	assert.Equal(t, []float64{
		5120,
		2048,
		4,
		2,
		500,
		500,
		50,
		0.2,
	}, metricTestValues(second))
	assert.Equal(t, 2, readIndex)
}

func TestIOTracingUpdateWarmsUpAfterInitialReadFailure(t *testing.T) {
	previousConfig := configSnapshot()
	Set(validIOTracingConfig())
	t.Cleanup(func() { Set(previousConfig) })
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(t.TempDir())
	t.Cleanup(func() {
		procfs.RootPrefix(originalPrefix)
	})

	attr, err := newIOTracing()
	require.NoError(t, err)
	tracer, ok := attr.TracingData.(*ioTracing)
	require.True(t, ok)
	assert.Nil(t, tracer.metricPrevious)

	startedAt := time.Unix(100, 0)
	reads := []*rawDiskstatsSnapshot{
		metricTestSnapshot(
			startedAt,
			"sda",
			8,
			0,
			&promblockdevice.IOStats{ReadIOs: 10},
		),
		metricTestSnapshot(
			startedAt.Add(time.Second),
			"sda",
			8,
			0,
			&promblockdevice.IOStats{ReadIOs: 14},
		),
	}
	readIndex := 0
	tracer.readMetricSnapshot = func() (*rawDiskstatsSnapshot, error) {
		current := reads[readIndex]
		readIndex++
		return current, nil
	}

	metrics, err := tracer.Update()
	assert.Nil(t, metrics)
	assert.ErrorIs(t, err, metric.ErrNoData)

	metrics, err = tracer.Update()
	require.NoError(t, err)
	assert.Equal(t, float64(4), metrics[2].Value)
}

func TestIOTracingUpdateKeepsBaselineAfterReadFailure(t *testing.T) {
	startedAt := time.Unix(100, 0)
	previous := metricTestSnapshot(
		startedAt,
		"sda",
		8,
		0,
		&promblockdevice.IOStats{ReadIOs: 10},
	)
	current := metricTestSnapshot(
		startedAt.Add(2*time.Second),
		"sda",
		8,
		0,
		&promblockdevice.IOStats{ReadIOs: 20},
	)
	readFailure := errors.New("temporary diskstats failure")
	readIndex := 0
	tracer := &ioTracing{
		metricPrevious: previous,
		readMetricSnapshot: func() (*rawDiskstatsSnapshot, error) {
			readIndex++
			if readIndex == 1 {
				return nil, readFailure
			}
			return current, nil
		},
	}

	metrics, err := tracer.Update()
	assert.Nil(t, metrics)
	assert.ErrorIs(t, err, readFailure)
	assert.Same(t, previous, tracer.metricPrevious)

	metrics, err = tracer.Update()
	require.NoError(t, err)
	assert.Equal(t, float64(5), metrics[2].Value)
}

func TestIOTracingUpdateAdvancesBaselineAfterInvalidWindow(t *testing.T) {
	startedAt := time.Unix(100, 0)
	previous := metricTestSnapshot(
		startedAt,
		"sda",
		8,
		0,
		&promblockdevice.IOStats{ReadIOs: 100},
	)
	reads := []*rawDiskstatsSnapshot{
		metricTestSnapshot(
			startedAt.Add(time.Second),
			"sda",
			9,
			0,
			&promblockdevice.IOStats{ReadIOs: 10},
		),
		metricTestSnapshot(
			startedAt.Add(2*time.Second),
			"sda",
			9,
			0,
			&promblockdevice.IOStats{ReadIOs: 14},
		),
	}
	readIndex := 0
	tracer := &ioTracing{
		metricPrevious: previous,
		readMetricSnapshot: func() (*rawDiskstatsSnapshot, error) {
			current := reads[readIndex]
			readIndex++
			return current, nil
		},
	}

	metrics, err := tracer.Update()
	assert.Nil(t, metrics)
	assert.ErrorIs(t, err, metric.ErrNoData)

	metrics, err = tracer.Update()
	require.NoError(t, err)
	assert.Equal(t, float64(4), metrics[2].Value)
}

func TestIOTracingUpdateExportsMDSnapshot(t *testing.T) {
	startedAt := time.Unix(100, 0)
	tracer := &ioTracing{
		metricPrevious: metricTestSnapshot(
			startedAt,
			"md0",
			9,
			0,
			&promblockdevice.IOStats{},
		),
		readMetricSnapshot: func() (*rawDiskstatsSnapshot, error) {
			return metricTestSnapshot(
				startedAt.Add(time.Second),
				"md0",
				9,
				0,
				&promblockdevice.IOStats{},
			), nil
		},
	}

	metrics, err := tracer.Update()
	require.NoError(t, err)
	assert.Len(t, metrics, 8)
}

func TestIOTracingRealDiskstatsMetrics(t *testing.T) {
	if os.Getenv("TEST_INTEGRATION") != "true" {
		t.Skip("Set TEST_INTEGRATION=true to run integration tests")
	}

	previous, err := readRawDiskstatsSnapshot()
	require.NoError(t, err)
	if len(previous.devices) == 0 {
		t.Skip("no supported whole disk found")
	}

	tracer := &ioTracing{metricPrevious: previous}
	time.Sleep(time.Second)
	metrics, err := tracer.Update()
	require.NoError(t, err)

	snapshot := buildDiskStatusSnapshot(previous, tracer.metricPrevious)
	device := strings.TrimSpace(os.Getenv("TEST_IOSTAT_DEVICE"))
	if device != "" {
		if _, ok := snapshot.devices[device]; !ok {
			t.Fatalf("TEST_IOSTAT_DEVICE %q is not a monitored whole disk", device)
		}
	}
	if len(snapshot.devices) == 0 {
		t.Skip("no stable supported whole disk found")
	}
	assert.Len(t, metrics, len(snapshot.devices)*8)
}

// Diskstats snapshots, elapsed time, and counter transitions.

func TestBuildDiskMetricRejectsInvalidWindows(t *testing.T) {
	previous := blockdevice.Diskstats{
		Info: promblockdevice.Info{
			MajorNumber: 8,
			MinorNumber: 0,
		},
		IOStats: promblockdevice.IOStats{
			ReadIOs:         10,
			ReadSectors:     100,
			ReadTicks:       20,
			WriteIOs:        10,
			WriteSectors:    100,
			WriteTicks:      20,
			IOsTotalTicks:   20,
			WeightedIOTicks: 20,
		},
	}

	tests := []struct {
		name   string
		mutate func(*blockdevice.Diskstats)
	}{
		{name: "major changed", mutate: func(s *blockdevice.Diskstats) { s.MajorNumber++ }},
		{name: "minor changed", mutate: func(s *blockdevice.Diskstats) { s.MinorNumber++ }},
		{name: "read IOs reset", mutate: func(s *blockdevice.Diskstats) { s.ReadIOs-- }},
		{name: "read sectors reset", mutate: func(s *blockdevice.Diskstats) { s.ReadSectors-- }},
		{name: "read ticks reset", mutate: func(s *blockdevice.Diskstats) { s.ReadTicks-- }},
		{name: "write IOs reset", mutate: func(s *blockdevice.Diskstats) { s.WriteIOs-- }},
		{name: "write sectors reset", mutate: func(s *blockdevice.Diskstats) { s.WriteSectors-- }},
		{name: "write ticks reset", mutate: func(s *blockdevice.Diskstats) { s.WriteTicks-- }},
		{name: "IO ticks reset", mutate: func(s *blockdevice.Diskstats) { s.IOsTotalTicks-- }},
		{name: "weighted IO ticks reset", mutate: func(s *blockdevice.Diskstats) { s.WeightedIOTicks-- }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			current := previous
			test.mutate(&current)
			_, ok := buildDiskMetric(&previous, &current, time.Second)
			assert.False(t, ok)
		})
	}
}

func TestBuildDiskStatusSnapshotDropsInvalidAndDisappearedDevices(t *testing.T) {
	start := time.Unix(100, 0)
	previous := &rawDiskstatsSnapshot{
		timestamp: start,
		devices: map[string]blockdevice.Diskstats{
			"sda": {
				Info: promblockdevice.Info{
					DeviceName:  "sda",
					MajorNumber: 8,
					MinorNumber: 0,
				},
			},
			"sdb": {
				Info: promblockdevice.Info{
					DeviceName:  "sdb",
					MajorNumber: 8,
					MinorNumber: 16,
				},
				IOStats: promblockdevice.IOStats{ReadSectors: 10},
			},
			"sdc": {
				Info: promblockdevice.Info{
					DeviceName:  "sdc",
					MajorNumber: 8,
					MinorNumber: 32,
				},
			},
		},
		order: []string{"sda", "sdb", "sdc"},
	}
	current := &rawDiskstatsSnapshot{
		timestamp: start.Add(time.Second),
		devices: map[string]blockdevice.Diskstats{
			"sda": {
				Info: promblockdevice.Info{
					DeviceName:  "sda",
					MajorNumber: 8,
					MinorNumber: 0,
				},
			},
			"sdb": {
				Info: promblockdevice.Info{
					DeviceName:  "sdb",
					MajorNumber: 8,
					MinorNumber: 16,
				},
				IOStats: promblockdevice.IOStats{ReadSectors: 9},
			},
		},
		order: []string{"sda", "sdb"},
	}

	got := buildDiskStatusSnapshot(previous, current)
	assert.Equal(t, []string{"sda"}, got.order)
	assert.Contains(t, got.devices, "sda")
	assert.NotContains(t, got.devices, "sdb")
	assert.NotContains(t, got.devices, "sdc")
}

func TestIsMonitoredDiskFiltersPartitionsAndPseudoDevices(t *testing.T) {
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(t.TempDir())
	t.Cleanup(func() {
		procfs.RootPrefix(originalPrefix)
	})

	wholeDevicePath := filepath.Join(
		procfs.DefaultPathByType("sys"),
		"dev",
		"block",
		"8:0",
	)
	require.NoError(t, os.MkdirAll(wholeDevicePath, 0o755))

	partitionPath := filepath.Join(
		procfs.DefaultPathByType("sys"),
		"dev",
		"block",
		"8:1",
	)
	require.NoError(t, os.MkdirAll(partitionPath, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(partitionPath, "partition"),
		[]byte("1\n"),
		0o600,
	))

	tests := []struct {
		name string
		stat *blockdevice.Diskstats
		want bool
	}{
		{
			name: "whole disk",
			stat: &blockdevice.Diskstats{
				Info: promblockdevice.Info{
					DeviceName:  "sda",
					MajorNumber: 8,
					MinorNumber: 0,
				},
			},
			want: true,
		},
		{
			name: "partition",
			stat: &blockdevice.Diskstats{
				Info: promblockdevice.Info{
					DeviceName:  "sda1",
					MajorNumber: 8,
					MinorNumber: 1,
				},
			},
		},
		{
			name: "loop",
			stat: &blockdevice.Diskstats{
				Info: promblockdevice.Info{DeviceName: "loop0"},
			},
		},
		{
			name: "ram",
			stat: &blockdevice.Diskstats{
				Info: promblockdevice.Info{DeviceName: "ram0"},
			},
		},
		{
			name: "zram",
			stat: &blockdevice.Diskstats{
				Info: promblockdevice.Info{DeviceName: "zram0"},
			},
		},
		{
			name: "floppy",
			stat: &blockdevice.Diskstats{
				Info: promblockdevice.Info{DeviceName: "fd0"},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, isMonitoredDisk(test.stat))
		})
	}
}

func TestMDSnapshotDoesNotTriggerDiagnostic(t *testing.T) {
	status := diskMetricStatus{IOUtil: 100}
	snapshot := &diskStatusSnapshot{
		devices: map[string]diskMetricStatus{"md0": status},
		order:   []string{"md0"},
	}
	raw := &rawDiskstatsSnapshot{
		devices: map[string]blockdevice.Diskstats{
			"md0": {
				Info: promblockdevice.Info{
					DeviceName:  "md0",
					MajorNumber: 9,
					MinorNumber: 0,
				},
			},
		},
	}

	lastMetrics := map[string]diskMetricStatus{"md0": status}
	reason := evaluateThresholds(
		raw,
		snapshot,
		lastMetrics,
		ioThresholds{UtilThreshold: 90},
	)

	assert.Nil(t, reason)
	assert.Contains(t, lastMetrics, "md0")
}

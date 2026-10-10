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
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/internal/procfs/blockdevice"
	"github.com/ccfos/huatuo/internal/tracing"
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

func assertDiskMetrics(t *testing.T, metrics []*metric.Data, device string, values []float64) {
	t.Helper()
	names := []string{
		"read_bytes_per_second",
		"write_bytes_per_second",
		"read_iops",
		"write_iops",
		"read_await_milliseconds",
		"write_await_milliseconds",
		"io_utilization_percent",
		"average_queue_size",
	}
	require.Len(t, metrics, len(names))
	require.Len(t, values, len(names))
	for i, name := range names {
		require.NotNil(t, metrics[i], name)
		assert.Equal(t, name, metrics[i].Name())
		assert.Equal(t, metric.MetricTypeGauge, metrics[i].Type(), name)
		assert.Equal(t, values[i], metrics[i].Value, name)
		assert.NotEmpty(t, metrics[i].Help(), name)
		labels := metrics[i].Labels()
		delete(labels, metric.LabelHost)
		delete(labels, metric.LabelRegion)
		assert.Equal(t, map[string]string{"device": device}, labels, name)
	}
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
	assert.Equal(t, tracing.FlagTracing|tracing.FlagMetric, attr.Flag)
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
	assertDiskMetrics(t, first, "sda", []float64{
		1280,
		1792,
		1.5,
		2.5,
		1250.33,
		2500.6,
		95,
		0.12,
	})

	second, err := tracer.Update()
	require.NoError(t, err)
	assertDiskMetrics(t, second, "sda", []float64{
		5120,
		2048,
		4,
		2,
		500,
		500,
		50,
		0.2,
	})
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
	assertDiskMetrics(t, metrics, "md0", make([]float64, 8))
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
				Info: promblockdevice.Info{DeviceName: "loop0", MajorNumber: 8},
			},
		},
		{
			name: "ram",
			stat: &blockdevice.Diskstats{
				Info: promblockdevice.Info{DeviceName: "ram0", MajorNumber: 8},
			},
		},
		{
			name: "zram",
			stat: &blockdevice.Diskstats{
				Info: promblockdevice.Info{DeviceName: "zram0", MajorNumber: 8},
			},
		},
		{
			name: "floppy",
			stat: &blockdevice.Diskstats{
				Info: promblockdevice.Info{DeviceName: "fd0", MajorNumber: 8},
			},
		},
	}

	// Pseudo names also resolve to the valid 8:0 whole-device fixture, so
	// missing sysfs cannot hide a regression in the name exclusion rule.
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

func TestIOTracingThresholdsRequireConsecutiveWindows(t *testing.T) {
	thresholds := ioThresholds{
		UtilThreshold:  90,
		RBPSThreshold:  1,
		WBPSThreshold:  1,
		AwaitThreshold: 10,
	}
	busy := diskMetricStatus{IOUtil: 90.5}
	nvmeRead := diskMetricStatus{IOUtil: 90.5, ReadBPS: 1024*1024 + 0.5}
	nvmeWrite := diskMetricStatus{IOUtil: 90.5, WriteBPS: 1024*1024 + 0.5}
	readAwait := diskMetricStatus{ReadAwait: 10.5}
	writeAwait := diskMetricStatus{WriteAwait: 10.5}
	tests := []struct {
		name     string
		device   string
		previous diskMetricStatus
		current  diskMetricStatus
		want     thresholdReason
	}{
		{"disk busy twice", "sda", busy, busy, ioReasonUtil},
		{"one busy window", "sda", diskMetricStatus{}, busy, ioReasonNone},
		{"busy then idle", "sda", busy, diskMetricStatus{}, ioReasonNone},
		{"util equals threshold", "sda", busy, diskMetricStatus{IOUtil: 90}, ioReasonNone},
		{"NVMe util alone", "nvme0n1", busy, busy, ioReasonNone},
		{"NVMe read throughput", "nvme0n1", nvmeRead, nvmeRead, ioReasonReadBPS},
		{"NVMe write throughput", "nvme0n1", nvmeWrite, nvmeWrite, ioReasonWriteBPS},
		{
			"NVMe throughput equals threshold", "nvme0n1", nvmeRead,
			diskMetricStatus{IOUtil: 90.5, ReadBPS: 1024 * 1024},
			ioReasonNone,
		},
		{
			"NVMe util equals threshold", "nvme0n1", nvmeRead,
			diskMetricStatus{IOUtil: 90, ReadBPS: 1024*1024 + 0.5},
			ioReasonNone,
		},
		{"read await", "sda", readAwait, readAwait, ioReasonReadAwait},
		{"write await", "nvme0n1", writeAwait, writeAwait, ioReasonWriteAwait},
		{"await equals threshold", "sda", readAwait, diskMetricStatus{ReadAwait: 10}, ioReasonNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := metricTestSnapshot(time.Unix(100, 0), test.device, 8, 0, &promblockdevice.IOStats{})
			lastMetrics := make(map[string]diskMetricStatus)
			for i, status := range []diskMetricStatus{test.previous, test.current} {
				snapshot := &diskStatusSnapshot{
					devices: map[string]diskMetricStatus{test.device: status},
					order:   []string{test.device},
				}
				reason := evaluateThresholds(raw, snapshot, lastMetrics, thresholds)
				if i == 0 || test.want == ioReasonNone {
					assert.Nil(t, reason)
					continue
				}
				require.NotNil(t, reason)
				assert.Equal(t, string(test.want), reason.Type)
				assert.Equal(t, test.device, reason.Device)
			}
		})
	}
}

func TestIOTracingDiagnosticRestartsAfterInvalidWindow(t *testing.T) {
	tests := []struct {
		name       string
		invalidate func(*blockdevice.Diskstats)
	}{
		{"major changed", func(stat *blockdevice.Diskstats) { stat.MajorNumber++ }},
		{"minor changed", func(stat *blockdevice.Diskstats) { stat.MinorNumber++ }},
		{"counter reset", func(stat *blockdevice.Diskstats) { stat.ReadIOs = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous := metricTestSnapshot(time.Unix(100, 0), "sda", 8, 0,
				&promblockdevice.IOStats{ReadIOs: 100, IOsTotalTicks: 1000})
			lastMetrics := make(map[string]diskMetricStatus)
			// A busy window, an invalid window, then two new busy windows.
			// Only the final window may trigger; old history cannot bridge the gap.
			for step := 0; step < 4; step++ {
				stat := previous.devices["sda"]
				stat.ReadIOs++
				stat.IOsTotalTicks += 950
				if step == 1 {
					test.invalidate(&stat)
				}
				current := metricTestSnapshot(previous.timestamp.Add(time.Second),
					"sda", stat.MajorNumber, stat.MinorNumber, &stat.IOStats)
				reason := evaluateThresholds(current,
					buildDiskStatusSnapshot(previous, current), lastMetrics,
					ioThresholds{UtilThreshold: 90})
				if step < 3 {
					assert.Nil(t, reason, "step %d", step)
				} else {
					require.NotNil(t, reason)
					assert.Equal(t, "ioutil", reason.Type)
					assert.Equal(t, "sda", reason.Device)
				}
				if step == 1 {
					assert.NotContains(t, lastMetrics, "sda")
				}
				previous = current
			}
		})
	}
}

func TestReasonSnapshotPreservesIntegerDiskStatusSchema(t *testing.T) {
	status := persistedDiskStatus(diskMetricStatus{
		ReadBPS:    1.75,
		ReadIOPS:   2.5,
		ReadAwait:  3.25,
		WriteBPS:   4.75,
		WriteIOPS:  5.5,
		WriteAwait: 6.25,
		IOUtil:     7.75,
		QueueSize:  8.5,
	})

	raw, err := json.Marshal(reasonSnapshot{
		Type:     string(ioReasonUtil),
		Device:   "sda",
		IOStatus: status,
	})
	require.NoError(t, err)
	assert.JSONEq(t,
		`{"type":"ioutil","device":"sda","major_num":0,"minor_num":0,"iostatus":`+
			`{"read_bps":1,"read_iops":2,"read_await":3,`+
			`"write_bps":4,"write_iops":5,"write_await":6,`+
			`"io_util":7,"queue_size":8},"summary":""}`,
		string(raw),
	)
}

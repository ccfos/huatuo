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
	"os"
	"path/filepath"
	"testing"
	"time"

	"huatuo-bamai/internal/procfs"
	"huatuo-bamai/internal/procfs/blockdevice"

	promblockdevice "github.com/prometheus/procfs/blockdevice"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildDiskMetricUsesActualElapsedTime(t *testing.T) {
	previous := blockdevice.Diskstats{
		Info: promblockdevice.Info{
			MajorNumber: 8,
			MinorNumber: 0,
		},
		IOStats: promblockdevice.IOStats{
			ReadIOs:         100,
			ReadSectors:     1000,
			ReadTicks:       200,
			WriteIOs:        50,
			WriteSectors:    500,
			WriteTicks:      100,
			IOsTotalTicks:   1000,
			WeightedIOTicks: 2000,
		},
	}
	current := previous
	current.ReadIOs += 4
	current.ReadSectors += 20
	current.ReadTicks += 12
	current.WriteIOs += 2
	current.WriteSectors += 8
	current.WriteTicks += 10
	current.IOsTotalTicks += 500
	current.WeightedIOTicks += 600

	got, ok := buildDiskMetric(&previous, &current, 2*time.Second)
	require.True(t, ok)
	assert.Equal(t, diskStatus{
		ReadBPS:    5120,
		ReadIOPS:   2,
		ReadAwait:  3,
		WriteBPS:   2048,
		WriteIOPS:  1,
		WriteAwait: 5,
		IOUtil:     25,
		QueueSize:  0.3,
	}, got)
}

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
	status := diskStatus{IOUtil: 100}
	snapshot := &diskStatusSnapshot{
		devices: map[string]diskStatus{"md0": status},
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

	reason, next := evaluateThresholds(
		raw,
		snapshot,
		map[string]diskStatus{"md0": status},
		ioThresholds{UtilThreshold: 90},
	)

	assert.Nil(t, reason)
	assert.Contains(t, next, "md0")
}

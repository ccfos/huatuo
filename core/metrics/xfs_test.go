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

// Exercise XFS metric values, snapshot ownership and device availability.
package collector

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/pkg/metric"
)

func newXFSFixture(t testing.TB, devices int) (*xfsCollector, string) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "proc"), 0o755))
	require.NoError(t, os.Mkdir(filepath.Join(root, "sys"), 0o755))
	for i := 0; i < devices; i++ {
		name := fmt.Sprintf("sda%d", i)
		for target, source := range map[string]string{
			filepath.Join("dev", name):                                  "dev/sda0",
			filepath.Join("sys/fs/xfs", name, "stats/stats"):            "sys/fs/xfs/sda0/stats/stats",
			filepath.Join("sys/fs/xfs", name, "log/log_tail_lsn"):       "sys/fs/xfs/sda0/log/log_tail_lsn",
			filepath.Join("sys/fs/xfs", name, "log/reserve_grant_head"): "sys/fs/xfs/sda0/log/reserve_grant_head",
		} {
			data, err := os.ReadFile(filepath.Join("..", "..", "integration", "fixtures", source))
			require.NoError(t, err)
			path := filepath.Join(root, target)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
			require.NoError(t, os.WriteFile(path, data, 0o600))
		}
	}

	originalPrefix := filepath.Dir(procfs.DefaultPath())
	originalLogSizes := xfsLogSizeMap
	procfs.RootPrefix(root)
	t.Cleanup(func() {
		procfs.RootPrefix(originalPrefix)
		xfsLogSizeMap = originalLogSizes
	})
	attr, err := newXFSCollector()
	require.NoError(t, err)
	return attr.TracingData.(*xfsCollector), root
}

func TestXFSUpdate(t *testing.T) {
	c, _ := newXFSFixture(t, 2)

	for update := 0; update < 2; update++ {
		metrics, err := c.Update()
		require.NoError(t, err)
		require.Len(t, metrics, 16)
		for offset := 0; offset < len(metrics); offset += metricsPerDevice {
			deviceMetrics := metrics[offset : offset+metricsPerDevice]
			name := deviceMetrics[0].Labels()["device"]
			names := []string{
				"log_free_bytes", "log_space_sleep_total", "alloc_blocks_total",
				"alloc_extents_total", "inode_missed_total", "inode_attempts_total",
				"buf_busy_locked_total", "buf_locked_waited_total",
			}
			for i, m := range deviceMetrics {
				require.Equal(t, names[i], m.Name())
				metricType := metric.MetricTypeCounter
				if m.Name() == "log_free_bytes" {
					metricType = metric.MetricTypeGauge
				}
				require.Equal(t, metricType, m.Type())
				require.Equal(t, name, m.Labels()["device"])
			}
			values := make([]float64, len(deviceMetrics))
			for i, m := range deviceMetrics {
				values[i] = m.Value
			}
			require.Equal(t, []float64{
				10466800, 1430, 201138642, 5044296, 7162023, 12131976, 629546, 444421,
			}, values, name)
		}
	}
}

func TestXFSUpdateSnapshotIsolation(t *testing.T) {
	c, root := newXFSFixture(t, 1)
	first, err := c.Update()
	require.NoError(t, err)
	require.Len(t, first, metricsPerDevice)
	values := make([]float64, len(first))
	for i, m := range first {
		values[i] = m.Value
	}

	path := filepath.Join(root, "sys/fs/xfs/sda0/stats/stats")
	stats, err := os.ReadFile(path)
	require.NoError(t, err)
	stats = []byte(strings.Replace(string(stats), "201138642", "201138643", 1))
	require.NoError(t, os.WriteFile(path, stats, 0o600))
	second, err := c.Update()
	require.NoError(t, err)
	require.Len(t, second, len(first))
	for i, m := range first {
		require.NotSame(t, m, second[i])
		require.Equal(t, values[i], m.Value)
	}
	require.Equal(t, float64(201138643), second[2].Value)
}

func TestXFSUpdateUnmountedDevices(t *testing.T) {
	c, root := newXFSFixture(t, 2)
	metrics, err := c.Update()
	require.NoError(t, err)
	require.Len(t, metrics, 16)
	require.NoError(t, os.Remove(filepath.Join(root, "sys/fs/xfs/sda0/stats/stats")))
	metrics, err = c.Update()
	require.NoError(t, err)
	require.Len(t, metrics, metricsPerDevice)
	for _, m := range metrics {
		require.Equal(t, "sda1", m.Labels()["device"])
	}
	require.NoError(t, os.Remove(filepath.Join(root, "sys/fs/xfs/sda1/stats/stats")))
	metrics, err = c.Update()
	require.NoError(t, err)
	require.Empty(t, metrics)
}

func TestXFSUpdateErrors(t *testing.T) {
	for _, tt := range []struct {
		name      string
		path      string
		content   string
		remove    bool
		reinit    bool
		fatal     bool
		headBytes string
	}{
		{name: "missing max size", path: "dev/sda0", remove: true, reinit: true},
		{name: "invalid max size", path: "dev/sda0", content: string(make([]byte, 512)), reinit: true},
		{name: "missing log", path: "sys/fs/xfs/sda0/log/log_tail_lsn", remove: true},
		{name: "invalid log", path: "sys/fs/xfs/sda0/log/reserve_grant_head", content: "invalid\n"},
		{name: "new log interface", path: "sys/fs/xfs/sda0/log/reserve_grant_head", remove: true, headBytes: "8192\n"},
		{name: "invalid stats", path: "sys/fs/xfs/sda0/stats/stats", content: "extent_alloc invalid\n", fatal: true},
		{name: "unreadable stats", path: "sys/fs/xfs/sda0/stats/stats", remove: true, fatal: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, root := newXFSFixture(t, 2)
			path := filepath.Join(root, tt.path)
			if tt.remove {
				require.NoError(t, os.Remove(path))
				if tt.fatal {
					require.NoError(t, os.Mkdir(path, 0o755))
				}
			} else {
				require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o600))
			}
			if tt.headBytes != "" {
				path := filepath.Join(root, "sys/fs/xfs/sda0/log/reserve_grant_head_bytes")
				require.NoError(t, os.WriteFile(path, []byte(tt.headBytes), 0o600))
			}
			if tt.reinit {
				attr, err := newXFSCollector()
				require.NoError(t, err)
				c = attr.TracingData.(*xfsCollector)
			}

			metrics, err := c.Update()
			if tt.fatal {
				require.Error(t, err)
				require.Nil(t, metrics)
				return
			}
			require.NoError(t, err)
			require.Len(t, metrics, 15)
			values := map[string]map[string]float64{
				"sda0": {}, "sda1": {},
			}
			for _, m := range metrics {
				values[m.Labels()["device"]][m.Name()] = m.Value
			}
			require.Len(t, values["sda0"], 7)
			require.NotContains(t, values["sda0"], "log_free_bytes")
			require.Len(t, values["sda1"], 8)
			require.Equal(t, float64(10466800), values["sda1"]["log_free_bytes"])
			for name, value := range values["sda0"] {
				require.Equal(t, values["sda1"][name], value)
			}
		})
	}
}

func TestXFSUpdateFullLog(t *testing.T) {
	c, root := newXFSFixture(t, 1)
	logPath := filepath.Join(root, "sys/fs/xfs/sda0/log")
	require.NoError(t, os.WriteFile(filepath.Join(logPath, "log_tail_lsn"), []byte("1:0\n"), 0o600))
	head := []byte(fmt.Sprintf("1:%.0f\n", xfsLogSizeMap["sda0"]))
	require.NoError(t, os.WriteFile(filepath.Join(logPath, "reserve_grant_head"), head, 0o600))
	metrics, err := c.Update()
	require.NoError(t, err)
	require.Len(t, metrics, metricsPerDevice)
	require.Equal(t, "log_free_bytes", metrics[0].Name())
	require.Zero(t, metrics[0].Value)
}

func TestXFSNoDevices(t *testing.T) {
	c, _ := newXFSFixture(t, 0)
	metrics, err := c.Update()
	require.NoError(t, err)
	require.Empty(t, metrics)
}

func TestXFSLogFreeBytes(t *testing.T) {
	for _, tt := range []struct {
		name string
		tail string
		head string
		want float64
	}{
		{name: "same cycle", tail: "1:8\n", head: "1:8192\n", want: 12288},
		{name: "next cycle", tail: "1:24\n", head: "2:4096\n", want: 8192},
		{name: "head more than one cycle ahead", tail: "1:24\n", head: "3:4096\n", want: 0},
		{name: "head cycle behind tail", tail: "2:24\n", head: "1:4096\n", want: 16384},
		{name: "head offset behind tail", tail: "1:24\n", head: "1:4096\n", want: 16384},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "log_tail_lsn"), []byte(tt.tail), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(root, "reserve_grant_head"), []byte(tt.head), 0o600))
			got, err := calcuXlogFreeBytes(root, 16384)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

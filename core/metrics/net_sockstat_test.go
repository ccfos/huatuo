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
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/pkg/metric"
)

const (
	sockstatIPv4Fixture = "sockets: used 25\nTCP: inuse 4 orphan 0 tw 2 alloc 8 mem 3\nUDP: inuse 2 mem 1\nFRAG: inuse 0 memory 0\n"
	sockstatIPv6Fixture = "TCP6: inuse 7\nUDP6: inuse 3\nUDPLITE6: inuse 0\nRAW6: inuse 1\nFRAG6: inuse 2 memory 4096\n"
)

func sockstatFixture(t testing.TB, pid int, v4, v6 string) string {
	t.Helper()
	root := t.TempDir()
	oldRoot := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(root)
	t.Cleanup(func() { procfs.RootPrefix(oldRoot) })
	dir := filepath.Join(root, "proc", strconv.Itoa(pid), "net")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for name, data := range map[string]string{"sockstat": v4, "sockstat6": v6} {
		if data != "" {
			require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600))
		}
	}
	return dir
}

func TestSockstatDualStack(t *testing.T) {
	for _, container := range []*pod.Container{nil, {InitPid: 42, Hostname: "pod-1", Labels: map[string]any{"HostNamespace": "test"}}} {
		name := "host"
		if container != nil {
			name = "container"
		}
		t.Run(name, func(t *testing.T) {
			sockstatFixture(t, container.InitPidOrInitnsPid(), sockstatIPv4Fixture, sockstatIPv6Fixture)
			data, err := (&sockstatCollector{}).procStatMetrics(container)
			require.NoError(t, err)
			values := make(map[string]float64)
			for _, m := range data {
				require.Equal(t, metric.MetricTypeGauge, m.Type())
				if container != nil {
					require.Equal(t, "pod-1", m.Labels()[metric.LabelContainerHost])
				}
				_, duplicate := values[strings.TrimPrefix(m.Name(), "container_")]
				require.False(t, duplicate, "duplicate metric %s", m.Name())
				values[strings.TrimPrefix(m.Name(), "container_")] = m.Value
			}
			require.Equal(t, float64(4), values["TCP_inuse"])
			require.Equal(t, float64(7), values["TCP6_inuse"])
			require.Equal(t, float64(3), values["UDP6_inuse"])
			require.Equal(t, float64(1), values["RAW6_inuse"])
			require.Equal(t, float64(2), values["FRAG6_inuse"])
			require.Equal(t, float64(4096), values["FRAG6_memory"])
			require.Equal(t, float64(25), values["sockets_used"])
			if container == nil {
				require.Equal(t, float64(3*os.Getpagesize()), values["TCP_mem_bytes"])
			} else {
				require.NotContains(t, values, "TCP_mem_bytes")
				require.NotContains(t, values, "TCP_mem")
			}
			require.NotContains(t, values, "TCP6_mem_bytes")
		})
	}
}

func TestSockstatOptionalFamilies(t *testing.T) {
	for _, tc := range []struct {
		name, v4, v6, metric string
		wantError            bool
	}{
		{name: "ipv4 only", v4: sockstatIPv4Fixture, metric: "TCP_inuse"},
		{name: "ipv6 only", v6: sockstatIPv6Fixture, metric: "TCP6_inuse"},
		{name: "neither family"},
		{name: "malformed ipv6", v4: sockstatIPv4Fixture, v6: "TCP6: inuse invalid\n", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sockstatFixture(t, 1, tc.v4, tc.v6)
			data, err := (&sockstatCollector{}).procStatMetrics(nil)
			if tc.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.metric == "" {
				require.Empty(t, data)
				return
			}
			var names []string
			for _, m := range data {
				names = append(names, m.Name())
			}
			require.Contains(t, names, tc.metric)
		})
	}
}

func BenchmarkSockstatDualStack(b *testing.B) {
	sockstatFixture(b, 1, sockstatIPv4Fixture, sockstatIPv6Fixture)
	c := &sockstatCollector{}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.procStatMetrics(nil); err != nil {
			b.Fatal(err)
		}
	}
}

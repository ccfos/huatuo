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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ccfos/huatuo/internal/procfs"
	"github.com/ccfos/huatuo/pkg/metric"
)

const numaFixture = "numa_hit 1000\nnuma_miss 12\nnuma_foreign 34\ninterleave_hit 56\nlocal_node 900\nother_node 112\n"

func numaTestRoot(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	previous := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(root)
	t.Cleanup(func() { procfs.RootPrefix(previous) })
	return filepath.Join(root, "sys/devices/system/node")
}

func writeNUMAFixture(t testing.TB, root, node, data string) string {
	t.Helper()
	dir := filepath.Join(root, node)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, "numastat")
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
	return path
}

func TestMemoryNUMACountersAndHotplug(t *testing.T) {
	root := numaTestRoot(t)
	path := writeNUMAFixture(t, root, "node0", numaFixture)
	writeNUMAFixture(t, root, "node7", "numa_hit 0\nother_node 99\nfuture_counter 12\n")
	attr, err := newMemoryNUMA()
	require.NoError(t, err)
	c := attr.TracingData.(*memoryNUMACollector)
	data, err := c.Update()
	require.NoError(t, err)
	require.Len(t, data, 8)
	values := make(map[string]float64)
	for _, m := range data {
		require.Equal(t, metric.MetricTypeCounter, m.Type())
		values[m.Labels()["node"]+"/"+m.Name()] = m.Value
	}
	require.Equal(t, map[string]float64{
		"0/numa_hit_pages_total": 1000, "0/numa_miss_pages_total": 12,
		"0/numa_foreign_pages_total": 34, "0/interleave_hit_pages_total": 56,
		"0/local_node_pages_total": 900, "0/other_node_pages_total": 112,
		"7/numa_hit_pages_total": 0, "7/other_node_pages_total": 99,
	}, values)
	require.NoError(t, os.Remove(path))
	writeNUMAFixture(t, root, "node42", "numa_hit 3\n")
	data, err = c.Update()
	require.NoError(t, err)
	require.Len(t, data, 3)
	for _, m := range data {
		require.NotEqual(t, "0", m.Labels()["node"])
	}
}

func TestMemoryNUMAOptionalAndPartialData(t *testing.T) {
	root := numaTestRoot(t)
	c := &memoryNUMACollector{}
	data, err := c.Update()
	require.ErrorIs(t, err, metric.ErrNoData)
	require.Empty(t, data)
	writeNUMAFixture(t, root, "node0", numaFixture)
	writeNUMAFixture(t, root, "node1", "numa_hit invalid\n")
	data, err = c.Update()
	require.ErrorContains(t, err, "node1/numastat")
	require.Len(t, data, 6)
}

func BenchmarkMemoryNUMA(b *testing.B) {
	root := numaTestRoot(b)
	for i := range 8 {
		writeNUMAFixture(b, root, fmt.Sprintf("node%d", i), numaFixture)
	}
	c := &memoryNUMACollector{}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Update(); err != nil {
			b.Fatal(err)
		}
	}
}

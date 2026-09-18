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
	"strings"
	"testing"

	"huatuo-bamai/internal/procfs"
	metricpkg "huatuo-bamai/pkg/metric"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSoftnetStat = `00000064 00000002 00000005 00000000 00000007 00000000 00000000 00000000 00000000
000000c8 00000000 00000000 00000000 0000000f 00000000 00000000 00000000 00000000
`

func newSoftnetTestCollector(t testing.TB, content string) *netSoftnet {
	t.Helper()

	tmpRoot := t.TempDir()
	netDir := filepath.Join(tmpRoot, "proc", "net")
	require.NoError(t, os.MkdirAll(netDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(netDir, "softnet_stat"), []byte(content), 0o600))

	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	procfs.RootPrefix(tmpRoot)

	return &netSoftnet{}
}

func TestNetSoftnetUpdate(t *testing.T) {
	collector := newSoftnetTestCollector(t, testSoftnetStat)

	metrics, err := collector.Update()
	require.NoError(t, err)
	require.Len(t, metrics, 8)

	expected := []struct {
		cpu        string
		name       string
		value      float64
		metricType int
	}{
		{cpu: "0", name: "processed_total", value: 100, metricType: metricpkg.MetricTypeCounter},
		{cpu: "0", name: "dropped_total", value: 2, metricType: metricpkg.MetricTypeCounter},
		{cpu: "0", name: "time_squeezed_total", value: 5, metricType: metricpkg.MetricTypeCounter},
		{cpu: "0", name: "received_rps_total", value: 7, metricType: metricpkg.MetricTypeCounter},
		{cpu: "1", name: "processed_total", value: 200, metricType: metricpkg.MetricTypeCounter},
		{cpu: "1", name: "dropped_total", value: 0, metricType: metricpkg.MetricTypeCounter},
		{cpu: "1", name: "time_squeezed_total", value: 0, metricType: metricpkg.MetricTypeCounter},
		{cpu: "1", name: "received_rps_total", value: 15, metricType: metricpkg.MetricTypeCounter},
	}
	for i, exp := range expected {
		assert.Equal(t, exp.name, metrics[i].Name())
		assert.Equal(t, exp.value, metrics[i].Value)
		assert.Equal(t, exp.metricType, metrics[i].Type())
		assert.Equal(t, exp.cpu, metrics[i].Labels()["cpu"])
	}
}

func TestParseSoftnetStatRejectsShortRow(t *testing.T) {
	_, err := parseSoftnetStat(strings.NewReader("00000064 00000002\n"))
	require.ErrorContains(t, err, "expected at least 9 hex columns")
}

func TestParseSoftnetStatRejectsInvalidHex(t *testing.T) {
	_, err := parseSoftnetStat(strings.NewReader(
		"00000064 00000002 00000005 00000000 000zzz 00000000 00000000 00000000 00000000\n"))
	require.ErrorContains(t, err, "parse received_rps")
}

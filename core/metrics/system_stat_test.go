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

	"huatuo-bamai/internal/procfs"
	metricpkg "huatuo-bamai/pkg/metric"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newSystemStatTestCollector(t testing.TB, stat string) *systemStat {
	t.Helper()

	tmpRoot := t.TempDir()
	procDir := filepath.Join(tmpRoot, "proc")
	require.NoError(t, os.MkdirAll(procDir, 0o755))
	if stat != "" {
		require.NoError(t, os.WriteFile(
			filepath.Join(procDir, "stat"), []byte(stat), 0o600))
	}

	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	procfs.RootPrefix(tmpRoot)

	procFS, err := procfs.NewDefaultFS()
	require.NoError(t, err)

	return &systemStat{procFS: procFS}
}

func TestSystemStatUpdate(t *testing.T) {
	// testStat is the /proc/stat fixture shared with disk_io_test.go.
	collector := newSystemStatTestCollector(t, testStat)

	metrics, err := collector.Update()
	require.NoError(t, err)
	require.Len(t, metrics, 6)

	expected := []struct {
		name       string
		value      float64
		metricType int
	}{
		{name: "context_switches_total", value: 200000, metricType: metricpkg.MetricTypeCounter},
		{name: "interrupts_total", value: 100000, metricType: metricpkg.MetricTypeCounter},
		{name: "processes_forked_total", value: 5000, metricType: metricpkg.MetricTypeCounter},
		{name: "procs_running", value: 3, metricType: metricpkg.MetricTypeGauge},
		{name: "procs_blocked", value: 1, metricType: metricpkg.MetricTypeGauge},
		{name: "boot_time_seconds", value: 1700000000, metricType: metricpkg.MetricTypeGauge},
	}
	for i, exp := range expected {
		assert.Equal(t, exp.name, metrics[i].Name())
		assert.Equal(t, exp.value, metrics[i].Value)
		assert.Equal(t, exp.metricType, metrics[i].Type())
	}
}

func TestSystemStatErrorWithoutProcStat(t *testing.T) {
	collector := newSystemStatTestCollector(t, "")

	_, err := collector.Update()
	require.Error(t, err)
}

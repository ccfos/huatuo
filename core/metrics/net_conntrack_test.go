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
	"huatuo-bamai/pkg/tracing"
	"huatuo-bamai/pkg/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testConntrackCount = "1234\n"
	testConntrackLimit = "262144\n"
)

// newConntrackTestRoot writes the conntrack sysctl fixture under a temp
// procfs root. When withConntrack is false the netfilter directory is left
// empty, mirroring a host without the conntrack module loaded.
func newConntrackTestRoot(t testing.TB, withConntrack bool) string {
	t.Helper()

	tmpRoot := t.TempDir()
	procDir := filepath.Join(tmpRoot, "proc")
	conntrackDir := filepath.Join(procDir, "sys", "net", "netfilter")
	require.NoError(t, os.MkdirAll(conntrackDir, 0o755))
	if withConntrack {
		require.NoError(t, os.WriteFile(
			filepath.Join(conntrackDir, "nf_conntrack_count"), []byte(testConntrackCount), 0o600))
		require.NoError(t, os.WriteFile(
			filepath.Join(conntrackDir, "nf_conntrack_max"), []byte(testConntrackLimit), 0o600))
	}

	originalPrefix := filepath.Dir(procfs.DefaultPath())
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })
	procfs.RootPrefix(tmpRoot)

	return conntrackDir
}

func TestNetConntrackUpdate(t *testing.T) {
	newConntrackTestRoot(t, true)

	attr, err := newNetConntrack()
	require.NoError(t, err)
	require.Equal(t, tracing.FlagMetric, attr.Flag)
	collector, ok := attr.TracingData.(*netConntrack)
	require.True(t, ok)

	metrics, err := collector.Update()
	require.NoError(t, err)
	require.Len(t, metrics, 3)

	expected := []struct {
		name       string
		value      float64
		metricType int
	}{
		{name: "entries", value: 1234, metricType: metricpkg.MetricTypeGauge},
		{name: "entries_limit", value: 262144, metricType: metricpkg.MetricTypeGauge},
		{name: "usage_percent", value: 1234.0 / 262144.0 * 100, metricType: metricpkg.MetricTypeGauge},
	}
	for i, exp := range expected {
		assert.Equal(t, exp.name, metrics[i].Name())
		assert.Equal(t, exp.value, metrics[i].Value)
		assert.Equal(t, exp.metricType, metrics[i].Type())
	}
}

// TestNetConntrackUsagePercentScale pins usage_percent to the repo-wide 0-100
// scale of the sibling *_percent metrics: a percentage alert threshold such
// as >90 must fire once the table is 91% full, which a 0-1 ratio (0.91)
// would never do.
func TestNetConntrackUsagePercentScale(t *testing.T) {
	conntrackDir := newConntrackTestRoot(t, true)
	require.NoError(t, os.WriteFile(
		filepath.Join(conntrackDir, "nf_conntrack_count"), []byte("91\n"), 0o600))
	require.NoError(t, os.WriteFile(
		filepath.Join(conntrackDir, "nf_conntrack_max"), []byte("100\n"), 0o600))

	collector := &netConntrack{}
	metrics, err := collector.Update()
	require.NoError(t, err)
	require.Len(t, metrics, 3)

	usage := metrics[2]
	assert.Equal(t, "usage_percent", usage.Name())
	assert.Equal(t, float64(91), usage.Value,
		"usage_percent must be 0-100 so percentage thresholds fire")
}

func TestNetConntrackNotSupported(t *testing.T) {
	newConntrackTestRoot(t, false)

	_, err := newNetConntrack()
	require.ErrorIs(t, err, types.ErrNotSupported)
}

func TestNetConntrackMalformedSysctl(t *testing.T) {
	conntrackDir := newConntrackTestRoot(t, true)
	require.NoError(t, os.WriteFile(
		filepath.Join(conntrackDir, "nf_conntrack_count"), []byte("1 2\n"), 0o600))

	collector := &netConntrack{}
	_, err := collector.Update()
	require.ErrorContains(t, err, "unexpected conntrack sysctl format")
}

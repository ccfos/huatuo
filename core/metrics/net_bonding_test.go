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

func bondingTestRoot(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	previous := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(root)
	t.Cleanup(func() { procfs.RootPrefix(previous) })
	return filepath.Join(root, "sys/class/net")
}

func writeBondFile(t testing.TB, root, path, data string) {
	t.Helper()
	path = filepath.Join(root, path)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(data), 0o600))
}

func bondMetricValues(data []*metric.Data) map[string]float64 {
	values := make(map[string]float64)
	for _, m := range data {
		labels := m.Labels()
		values[labels["master"]+"/"+labels["slave"]+"/"+m.Name()] = m.Value
	}
	return values
}

func TestBondingRedundancyAndHotplug(t *testing.T) {
	root := bondingTestRoot(t)
	writeBondFile(t, root, "bond0/bonding/slaves", "eth0 eth1\n")
	writeBondFile(t, root, "bond0/lower_eth0/bonding_slave/mii_status", "up\n")
	writeBondFile(t, root, "bond0/lower_eth0/bonding_slave/link_failure_count", "0\n")
	writeBondFile(t, root, "bond0/lower_eth1/bonding_slave/mii_status", "down\n")
	writeBondFile(t, root, "bond0/lower_eth1/bonding_slave/link_failure_count", "5\n")
	attr, err := newBonding()
	require.NoError(t, err)
	c := attr.TracingData.(*bondingCollector)
	data, err := c.Update()
	require.NoError(t, err)
	require.Equal(t, map[string]float64{
		"bond0//slaves": 2, "bond0//slaves_up": 1,
		"bond0/eth0/slave_up": 1, "bond0/eth1/slave_up": 0,
		"bond0/eth0/slave_link_failures_total": 0, "bond0/eth1/slave_link_failures_total": 5,
	}, bondMetricValues(data))
	for _, m := range data {
		if m.Name() == "slave_link_failures_total" {
			require.Equal(t, metric.MetricTypeCounter, m.Type())
		} else {
			require.Equal(t, metric.MetricTypeGauge, m.Type())
		}
	}
	// Recovery is observed without restarting the collector.
	writeBondFile(t, root, "bond0/lower_eth1/bonding_slave/mii_status", "up\n")
	data, err = c.Update()
	require.NoError(t, err)
	require.Equal(t, float64(2), bondMetricValues(data)["bond0//slaves_up"])
	require.NoError(t, os.RemoveAll(filepath.Join(root, "bond0")))
	writeBondFile(t, root, "bond7/bonding/slaves", "\n")
	data, err = c.Update()
	require.NoError(t, err)
	require.Equal(t, map[string]float64{"bond7//slaves": 0, "bond7//slaves_up": 0}, bondMetricValues(data))
}

func TestBondingMissingAndPartialData(t *testing.T) {
	root := bondingTestRoot(t)
	c := &bondingCollector{}
	_, err := c.Update()
	require.ErrorIs(t, err, metric.ErrNoData)
	writeBondFile(t, root, "bond0/bonding/slaves", "eth0 eth1\n")
	writeBondFile(t, root, "bond0/lower_eth0/bonding_slave/mii_status", "up\n")
	writeBondFile(t, root, "bond0/lower_eth0/bonding_slave/link_failure_count", "2\n")
	// eth1 disappeared between the membership and state reads.
	data, err := c.Update()
	require.NoError(t, err)
	values := bondMetricValues(data)
	require.Equal(t, float64(1), values["bond0/eth0/slave_up"])
	require.NotContains(t, values, "bond0//slaves_up")
	require.NotContains(t, values, "bond0/eth1/slave_up")
	writeBondFile(t, root, "bond0/lower_eth1/bonding_slave/mii_status", "unknown\n")
	writeBondFile(t, root, "bond1/bonding/slaves", "\n")
	data, err = c.Update()
	require.ErrorContains(t, err, "eth1")
	require.Contains(t, bondMetricValues(data), "bond1//slaves_up")
	require.NotContains(t, bondMetricValues(data), "bond0//slaves_up")
}

func BenchmarkBonding(b *testing.B) {
	root := bondingTestRoot(b)
	for i := range 4 {
		bond := fmt.Sprintf("bond%d", i)
		writeBondFile(b, root, bond+"/bonding/slaves", "eth0 eth1\n")
		for _, slave := range []string{"eth0", "eth1"} {
			writeBondFile(b, root, bond+"/lower_"+slave+"/bonding_slave/mii_status", "up\n")
			writeBondFile(b, root, bond+"/lower_"+slave+"/bonding_slave/link_failure_count", "0\n")
		}
	}
	c := &bondingCollector{}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := c.Update(); err != nil {
			b.Fatal(err)
		}
	}
}

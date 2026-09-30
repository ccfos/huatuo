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

func cpuFrequencyTestRoot(t testing.TB) string {
	t.Helper()
	root := t.TempDir()
	previous := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(root)
	t.Cleanup(func() { procfs.RootPrefix(previous) })
	return filepath.Join(root, "sys/devices/system/cpu")
}

func writeFrequencyFile(t testing.TB, root, policy, file, value string) {
	t.Helper()
	dir := filepath.Join(root, "cpufreq", policy)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, file), []byte(value+"\n"), 0o600))
}

func writeFrequencyPolicy(t testing.TB, root, policy string) {
	t.Helper()
	for file, value := range map[string]string{
		"scaling_cur_freq": "1800123", "scaling_min_freq": "800000", "scaling_max_freq": "2200000",
		"cpuinfo_min_freq": "400000", "cpuinfo_max_freq": "3200000", "bios_limit": "2500000",
		"scaling_governor": "schedutil", "scaling_driver": "acpi-cpufreq",
	} {
		writeFrequencyFile(t, root, policy, file, value)
	}
}

func TestCPUFrequencySharedPolicyAndHotplug(t *testing.T) {
	root := cpuFrequencyTestRoot(t)
	writeFrequencyPolicy(t, root, "policy7")
	for _, cpu := range []string{"cpu7", "cpu42"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, cpu), 0o755))
		require.NoError(t, os.Symlink("../cpufreq/policy7", filepath.Join(root, cpu, "cpufreq")))
	}
	attr, err := newCPUFrequency()
	require.NoError(t, err)
	c := attr.TracingData.(*cpuFrequencyCollector)
	data, err := c.Update()
	require.NoError(t, err)
	require.Len(t, data, 7)
	values := make(map[string]float64)
	for _, m := range data {
		require.Equal(t, "7", m.Labels()["policy"])
		require.Equal(t, metric.MetricTypeGauge, m.Type())
		values[m.Name()] = m.Value
		if m.Name() == "info" {
			require.Equal(t, "schedutil", m.Labels()["governor"])
			require.Equal(t, "acpi-cpufreq", m.Labels()["driver"])
		}
	}
	require.Equal(t, map[string]float64{
		"scaling_current_hertz": 1800123000, "scaling_minimum_hertz": 800000000,
		"scaling_maximum_hertz": 2200000000, "hardware_minimum_hertz": 400000000,
		"hardware_maximum_hertz": 3200000000, "bios_limit_hertz": 2500000000, "info": 1,
	}, values)
	writeFrequencyFile(t, root, "policy7", "scaling_max_freq", "1600000")
	data, err = c.Update()
	require.NoError(t, err)
	for _, m := range data {
		if m.Name() == "scaling_maximum_hertz" {
			require.Equal(t, float64(1600000000), m.Value)
		}
	}
	require.NoError(t, os.RemoveAll(filepath.Join(root, "cpufreq", "policy7")))
	writeFrequencyFile(t, root, "policy99", "scaling_cur_freq", "1234000")
	data, err = c.Update()
	require.NoError(t, err)
	require.Len(t, data, 1)
	require.Equal(t, "99", data[0].Labels()["policy"])
}

func TestCPUFrequencyOptionalAndPartialData(t *testing.T) {
	root := cpuFrequencyTestRoot(t)
	c := &cpuFrequencyCollector{}
	data, err := c.Update()
	require.ErrorIs(t, err, metric.ErrNoData)
	require.Empty(t, data)
	writeFrequencyFile(t, root, "policy0", "scaling_cur_freq", "invalid")
	writeFrequencyFile(t, root, "policy0", "scaling_max_freq", "2400000")
	writeFrequencyFile(t, root, "policy8", "scaling_cur_freq", "1234000")
	data, err = c.Update()
	require.ErrorContains(t, err, "policy 0 scaling_cur_freq")
	require.Len(t, data, 2)
	for _, m := range data {
		require.NotZero(t, m.Value)
	}
}

func BenchmarkCPUFrequency(b *testing.B) {
	for _, policies := range []int{8, 64} {
		b.Run(fmt.Sprintf("policies%d", policies), func(b *testing.B) {
			root := cpuFrequencyTestRoot(b)
			for i := range policies {
				writeFrequencyPolicy(b, root, fmt.Sprintf("policy%d", i))
			}
			c := &cpuFrequencyCollector{}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := c.Update(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

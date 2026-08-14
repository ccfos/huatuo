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

package v1

import (
	"os"
	"path/filepath"
	"testing"

	"huatuo-bamai/internal/cgroups/paths"
	"huatuo-bamai/internal/cgroups/subsystem"
)

func TestCpuUsageReadsCPUAcctSubsystem(t *testing.T) {
	root := t.TempDir()
	oldRoot := paths.RootfsDefaultPath
	paths.RootfsDefaultPath = root
	t.Cleanup(func() { paths.RootfsDefaultPath = oldRoot })

	path := filepath.Join("test", "usage")
	writeCgroupFile(t, paths.Path(subsystem.SubsystemCPUAcct, path, "cpuacct.stat"), "user 100\nsystem 50\n")
	writeCgroupFile(t, paths.Path(subsystem.SubsystemCPUAcct, path, "cpuacct.usage"), "1500000\n")

	usage, err := (&CgroupV1{}).CpuUsage(path)
	if err != nil {
		t.Fatal(err)
	}

	wantUser := 100 * microsecondsInSecond / clockTicks
	wantSystem := 50 * microsecondsInSecond / clockTicks
	if usage.User != wantUser || usage.System != wantSystem || usage.Usage != 1500 {
		t.Errorf("CpuUsage() = %+v, want User=%d System=%d Usage=1500", usage, wantUser, wantSystem)
	}
}

func TestCpuQuotaAndPeriodEffectiveCPUCount(t *testing.T) {
	root := t.TempDir()
	oldRoot := paths.RootfsDefaultPath
	paths.RootfsDefaultPath = root
	t.Cleanup(func() { paths.RootfsDefaultPath = oldRoot })

	tests := []struct {
		name   string
		cpuset string
		want   uint64
	}{
		{name: "cpuset", cpuset: "0-3", want: 4},
		{name: "missing", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join("test", tt.name)
			writeCgroupFile(t, paths.Path(subsystem.SubsystemCPU, path, "cpu.cfs_period_us"), "100000\n")
			writeCgroupFile(t, paths.Path(subsystem.SubsystemCPU, path, "cpu.cfs_quota_us"), "-1\n")
			if tt.cpuset != "" {
				writeCgroupFile(t, paths.Path(subsystem.SubsystemCPUSet, path, "cpuset.cpus"), tt.cpuset+"\n")
			}

			quota, err := (&CgroupV1{}).CpuQuotaAndPeriod(path)
			if err != nil {
				t.Fatal(err)
			}
			if quota.EffectiveCPUCount != tt.want {
				t.Errorf("EffectiveCPUCount = %d, want %d", quota.EffectiveCPUCount, tt.want)
			}
		})
	}
}

func writeCgroupFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

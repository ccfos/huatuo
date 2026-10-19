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
	"maps"
	"os"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
)

func TestCpuStatRaw(t *testing.T) {
	oldRoot := paths.RootfsDefaultPath
	paths.RootfsDefaultPath = t.TempDir()
	t.Cleanup(func() { paths.RootfsDefaultPath = oldRoot })

	const stat = "nr_periods 12\nnr_throttled 3\nthrottled_time 5000\nwait_sum 9000\nnr_burst 2\nburst_time 7000\n"
	want := map[string]uint64{
		"nr_periods":     12,
		"nr_throttled":   3,
		"throttled_time": 5000,
		"wait_sum":       9000,
		"nr_burst":       2,
		"burst_time":     7000,
	}
	for _, tc := range []struct {
		name, extra string
		wantErr     bool
	}{
		{"standard", "", false},
		{"unlimited", "current_bw -1\n", false},
		{"limited", "current_bw 5000000\n", false},
		{"unsigned-unlimited", "current_bw 18446744073709551615\n", false},
		{"negative-counter", "current_bw -1\nnr_throttled -1\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeCgroupFile(t, paths.Path(subsystem.SubsystemCPU, tc.name, "cpu.stat"), stat+tc.extra)
			got, err := (&CgroupV1{}).CpuStatRaw(tc.name)
			if (err != nil) != tc.wantErr {
				t.Fatalf("CpuStatRaw() error=%v, wantErr=%t", err, tc.wantErr)
			}
			if !tc.wantErr && !maps.Equal(got, want) {
				t.Fatalf("CpuStatRaw()=%v, want %v", got, want)
			}
		})
	}
}

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

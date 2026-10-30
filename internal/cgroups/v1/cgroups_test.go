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
	"bufio"
	"errors"
	"maps"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/internal/cgroups/paths"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
)

func TestCpuStatRaw(t *testing.T) {
	oldRoot := paths.RootfsDefaultPath
	paths.RootfsDefaultPath = t.TempDir()
	t.Cleanup(func() { paths.RootfsDefaultPath = oldRoot })

	for _, tc := range []struct {
		name    string
		content string
		want    map[string]uint64
		wantErr bool
		cause   error
	}{
		{
			name: "standard counters",
			content: "nr_periods 12\nnr_throttled 3\nthrottled_time 5000\n" +
				"wait_sum 9000\nnr_burst 2\nburst_time 7000\n",
			want: map[string]uint64{
				"nr_periods":     12,
				"nr_throttled":   3,
				"throttled_time": 5000,
				"wait_sum":       9000,
				"nr_burst":       2,
				"burst_time":     7000,
			},
		},
		{
			name: "negative fields among valid counters",
			content: "current_bw -1\nnr_periods 12\nfuture_field -2\n" +
				"nr_throttled 3\nwait_sum -9223372036854775808\n",
			want: map[string]uint64{
				"nr_periods":   12,
				"nr_throttled": 3,
			},
		},
		{
			name:    "nonnegative vendor fields",
			content: "current_bw 5000000\nfuture_field 0\nmaximum 18446744073709551615\n",
			want: map[string]uint64{
				"current_bw":   5000000,
				"future_field": 0,
				"maximum":      math.MaxUint64,
			},
		},
		{
			name:    "whitespace and missing final newline",
			content: "  current_bw\t-2 \n nr_throttled\t0 ",
			want:    map[string]uint64{"nr_throttled": 0},
		},
		{
			name:    "negative duplicate removes previous value",
			content: "nr_throttled 3\nnr_throttled -2\n",
			want:    map[string]uint64{},
		},
		{
			name:    "valid duplicate restores value",
			content: "nr_throttled -2\nnr_throttled 3\nnr_throttled 4\n",
			want:    map[string]uint64{"nr_throttled": 4},
		},
		{
			name:    "all negative",
			content: "current_bw -1\nnr_throttled -2\n",
			want:    map[string]uint64{},
		},
		{
			name: "empty",
			want: map[string]uint64{},
		},
		{
			name:    "invalid counter after negative field",
			content: "current_bw -1\nnr_throttled invalid\n",
			wantErr: true,
			cause:   strconv.ErrSyntax,
		},
		{
			name:    "invalid negative value",
			content: "current_bw -abc\n",
			wantErr: true,
			cause:   strconv.ErrSyntax,
		},
		{
			name:    "negative fractional value",
			content: "current_bw -1.5\n",
			wantErr: true,
			cause:   strconv.ErrSyntax,
		},
		{
			name:    "signed overflow",
			content: "current_bw -9223372036854775809\n",
			wantErr: true,
		},
		{
			name:    "unsigned overflow",
			content: "nr_throttled 18446744073709551616\n",
			wantErr: true,
			cause:   strconv.ErrRange,
		},
		{
			name:    "missing value",
			content: "current_bw\n",
			wantErr: true,
		},
		{
			name:    "extra fields",
			content: "current_bw -1 extra\n",
			wantErr: true,
		},
		{
			name:    "blank line",
			content: "\n",
			wantErr: true,
		},
		{
			name:    "scanner failure after valid counter",
			content: "nr_throttled 3\n" + strings.Repeat("x", bufio.MaxScanTokenSize),
			wantErr: true,
			cause:   bufio.ErrTooLong,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeCgroupFile(t, paths.Path(subsystem.SubsystemCPU, tc.name, "cpu.stat"), tc.content)
			got, err := (&CgroupV1{}).CpuStatRaw(tc.name)
			if (err != nil) != tc.wantErr {
				t.Fatalf("CpuStatRaw() error = %v, wantErr %t", err, tc.wantErr)
			}

			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("CpuStatRaw() error = %v, want cause %v", err, tc.cause)
			}

			if tc.wantErr {
				if got != nil {
					t.Fatalf("CpuStatRaw() = %v on error, want nil", got)
				}

				return
			}

			if got == nil || !maps.Equal(got, tc.want) {
				t.Fatalf("CpuStatRaw() = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("missing file", func(t *testing.T) {
		_, err := (&CgroupV1{}).CpuStatRaw("missing")
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("CpuStatRaw() error = %v, want file not found", err)
		}
	})
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

func TestCpuQuotaAndPeriodRejectsInvalidNegativeQuota(t *testing.T) {
	root := t.TempDir()
	oldRoot := paths.RootfsDefaultPath
	paths.RootfsDefaultPath = root
	t.Cleanup(func() { paths.RootfsDefaultPath = oldRoot })

	path := filepath.Join("test", "invalid-quota")
	writeCgroupFile(t, paths.Path(subsystem.SubsystemCPU, path, "cpu.cfs_period_us"), "100000\n")
	writeCgroupFile(t, paths.Path(subsystem.SubsystemCPU, path, "cpu.cfs_quota_us"), "-2\n")

	if _, err := (&CgroupV1{}).CpuQuotaAndPeriod(path); err == nil {
		t.Fatal("CpuQuotaAndPeriod() accepted an invalid negative quota")
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

func BenchmarkCpuStatRaw(b *testing.B) {
	oldRoot := paths.RootfsDefaultPath
	paths.RootfsDefaultPath = b.TempDir()
	b.Cleanup(func() { paths.RootfsDefaultPath = oldRoot })

	const counters = "nr_periods 12\nnr_throttled 3\nthrottled_time 5000\n" +
		"wait_sum 9000\nnr_burst 2\nburst_time 7000\n"
	for _, tc := range []struct {
		name    string
		content string
	}{
		{
			name:    "standard",
			content: counters,
		},
		{
			name:    "negative",
			content: "current_bw -1\n" + counters + "future_field -2\n",
		},
	} {
		b.Run(tc.name, func(b *testing.B) {
			path := paths.Path(subsystem.SubsystemCPU, tc.name, "cpu.stat")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				b.Fatal(err)
			}

			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				b.Fatal(err)
			}

			cgroup := &CgroupV1{}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := cgroup.CpuStatRaw(tc.name); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

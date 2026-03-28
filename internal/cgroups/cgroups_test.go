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

// Package testutil provides shared test helpers and a common test suite for
// the v1 and v2 cgroup sub-packages. It is not intended for use outside tests.

package cgroups

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"huatuo-bamai/internal/cgroups/paths"
	v2 "huatuo-bamai/internal/cgroups/v2"

	"github.com/opencontainers/runtime-spec/specs-go"
)

// RequireRoot if the process is not running as root. Returns false
// TODO: move the func to internal/testutils
func RequireRoot(tb testing.TB) bool {
	return os.Geteuid() == 0
}

func SetupRuntimeCgroupWithClean(t *testing.T) (Cgroup, string) {
	t.Helper()
	cgr, err := NewManager()
	if err != nil {
		t.Errorf("New: %v", err)
		return nil, ""
	}

	// UnixNano suffix makes the path unique across concurrent test runs.
	// path not including '-' which leads to unwanted path
	runtimePath := fmt.Sprintf("huatuo_dev_task_2026_%d", time.Now().UnixNano())
	if err := cgr.NewRuntime(runtimePath, &specs.LinuxResources{}); err != nil {
		t.Fatalf("NewRuntime(%q): %v", runtimePath, err)
	}

	t.Cleanup(func() {
		if err := cgr.DeleteRuntime(); err != nil {
			t.Fatalf("DeleteRuntime cleanup: %v", err)
		}
	})

	// v2 runtimePath of Unified need to add slice
	if CgroupMode() == Unified {
		return cgr, runtimePath + v2.Postfix
	}
	return cgr, runtimePath
}

// TestToSpec covers the primary resource-translation paths of ToSpec.
func TestToSpec(t *testing.T) {
	// cpuPeriod is the package-level constant (100000).
	const period uint64 = 100000

	testCases := []struct {
		name     string
		cpu      float64
		memory   int64
		validate func(t *testing.T, got *specs.LinuxResources)
	}{
		{
			name:   "cpu-only-one-core",
			cpu:    1.0,
			memory: 0,
			validate: func(t *testing.T, got *specs.LinuxResources) {
				if got.CPU == nil {
					t.Fatalf("CPU: got nil, want non-nil")
				}
				wantQuota := int64(period) // 1.0 × 100000
				if got.CPU.Quota == nil || *got.CPU.Quota != wantQuota {
					t.Errorf("CPU.Quota: got %v, want %d", got.CPU.Quota, wantQuota)
				}
				if got.CPU.Period == nil || *got.CPU.Period != period {
					t.Errorf("CPU.Period: got %v, want %d", got.CPU.Period, period)
				}
				if got.Memory != nil {
					t.Errorf("Memory: got %v, want nil (no memory requested)", got.Memory)
				}
			},
		},
		{
			name:   "cpu-half-core",
			cpu:    0.5,
			memory: 0,
			validate: func(t *testing.T, got *specs.LinuxResources) {
				if got.CPU == nil {
					t.Fatalf("CPU: got nil, want non-nil")
				}
				// 0.5 × 100000 = 50000
				wantQuota := int64(50000)
				if got.CPU.Quota == nil || *got.CPU.Quota != wantQuota {
					t.Errorf("CPU.Quota: got %v, want %d", got.CPU.Quota, wantQuota)
				}
			},
		},
		{
			name:   "memory-only-512mb",
			cpu:    0,
			memory: 512 * 1024 * 1024, // 512 MiB — representative container limit
			validate: func(t *testing.T, got *specs.LinuxResources) {
				if got.Memory == nil {
					t.Fatalf("Memory: got nil, want non-nil")
				}
				wantMem := int64(512 * 1024 * 1024)
				if got.Memory.Limit == nil || *got.Memory.Limit != wantMem {
					t.Errorf("Memory.Limit: got %v, want %d", got.Memory.Limit, wantMem)
				}
				if got.CPU != nil {
					t.Errorf("CPU: got %v, want nil (no cpu requested)", got.CPU)
				}
			},
		},
		{
			name:   "cpu-and-memory-two-cores-1gb",
			cpu:    2.0,
			memory: 1024 * 1024 * 1024, // 1 GiB
			validate: func(t *testing.T, got *specs.LinuxResources) {
				if got.CPU == nil {
					t.Errorf("CPU: got nil, want non-nil")
				} else {
					wantQuota := int64(200000) // 2.0 × 100000
					if got.CPU.Quota == nil || *got.CPU.Quota != wantQuota {
						t.Errorf("CPU.Quota: got %v, want %d", got.CPU.Quota, wantQuota)
					}
				}
				if got.Memory == nil {
					t.Errorf("Memory: got nil, want non-nil")
				} else {
					wantMem := int64(1024 * 1024 * 1024)
					if got.Memory.Limit == nil || *got.Memory.Limit != wantMem {
						t.Errorf("Memory.Limit: got %v, want %d", got.Memory.Limit, wantMem)
					}
				}
			},
		},
		{
			// Zero for both means no resource constraints requested; spec should
			// have nil CPU and nil Memory fields.
			name:   "zero-cpu-zero-memory-no-constraints",
			cpu:    0,
			memory: 0,
			validate: func(t *testing.T, got *specs.LinuxResources) {
				if got.CPU != nil {
					t.Errorf("CPU: got %v, want nil (zero cpu should not set field)", got.CPU)
				}
				if got.Memory != nil {
					t.Errorf("Memory: got %v, want nil (zero memory should not set field)", got.Memory)
				}
			},
		},
		{
			// Fractional CPUs below a full period slot; verifies truncation via
			// int64 conversion rather than rounding.
			name:   "cpu-fractional-below-one-period-unit",
			cpu:    0.000001, // 0.1 μ-core → quota = 0 after int64 truncation
			memory: 0,
			validate: func(t *testing.T, got *specs.LinuxResources) {
				if got.CPU == nil {
					t.Fatalf("CPU: got nil, want non-nil")
				}
				// int64(0.000001 × 100000) = int64(0.1) = 0
				wantQuota := int64(0)
				if got.CPU.Quota == nil || *got.CPU.Quota != wantQuota {
					t.Errorf("CPU.Quota: got %v, want %d (truncation expected)", got.CPU.Quota, wantQuota)
				}
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := ToSpec(tc.cpu, tc.memory)
			if got == nil {
				t.Fatalf("ToSpec returned nil, want non-nil *specs.LinuxResources")
			}
			tc.validate(t, got)
		})
	}
}

// TestRootFsFilePath verifies filepath.Join semantics against the rootfs base,
// including the RootfsDefaultPath() accessor (empty-subsystem case).
func TestRootFsFilePath(t *testing.T) {
	base := paths.RootfsDefaultPath

	testCases := []struct {
		subsys string
		want   string
	}{
		{"cpu", filepath.Join(base, "cpu")},
		{"memory", filepath.Join(base, "memory")},
		{"huatuo-dev/cpu", filepath.Join(base, "huatuo-dev/cpu")}, // nested — tests Join normalisation
		{"", base}, // empty subsys returns base; also validates RootfsDefaultPath()
	}

	for _, tc := range testCases {
		t.Run(tc.subsys+"-subsystem", func(t *testing.T) {
			if got := RootFsFilePath(tc.subsys); got != tc.want {
				t.Errorf("RootFsFilePath(%q): got %q, want %q", tc.subsys, got, tc.want)
			}
		})
	}

	// RootfsDefaultPath() must match the base used above.
	if got := RootfsDefaultPath(); got != base {
		t.Errorf("RootfsDefaultPath(): got %q, want %q", got, base)
	}
}

// TestCgroupManager verifies NewCgroupManager and CgroupMode together against
// the real host hierarchy. Both functions read extcgroups.Mode() so a single
// call is shared to keep assertions consistent.
func TestCgroupManager(t *testing.T) {
	mode := CgroupMode()

	// Skip if cgroup mode is unavailable or unknown on this host.
	if mode > Unified || mode <= Unavailable {
		t.Fatalf("CgroupMode(): got %d, not a known Mode constant", int(mode))
	}

	mgr, err := NewManager()
	if err != nil {
		t.Fatalf("NewCgroupManager() mode %v: got error %v, want nil", mode, err)
	}
	if mgr == nil {
		t.Fatalf("NewCgroupManager() mode %v: got nil, want non-nil Cgroup", mode)
	}
}

func TestCgroupInterfaces(t *testing.T) {
	if !RequireRoot(t) {
		t.Fatalf("Cgroup test requires root privileges")
	}

	cgr, err := NewManager()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Run("Name", func(t *testing.T) {
		if cgr.Name() == "" {
			t.Errorf("Name: got empty string, want non-empty")
		}
	})

	t.Run("NewAndDeleteRuntime", func(t *testing.T) {
		path := fmt.Sprintf("huatuo_dev_task_2026_delete_%d", time.Now().UnixNano())
		if err := cgr.NewRuntime(path, &specs.LinuxResources{}); err != nil {
			t.Fatalf("NewRuntime(%q): %v", path, err)
		}
		if err := cgr.DeleteRuntime(); err != nil {
			t.Fatalf("DeleteRuntime: %v", err)
		}
		if err := cgr.DeleteRuntime(); err == nil {
			t.Errorf("DeleteRuntime second call: got nil error, want non-nil, %v", err)
		}
	})

	cgr, runtimePath := SetupRuntimeCgroupWithClean(t)
	if cgr == nil {
		t.Fatalf("Setup runtime failed")
	}
	t.Run("UpdateRuntime", func(t *testing.T) {
		shares := uint64(1024)
		if err := cgr.UpdateRuntime(&specs.LinuxResources{
			CPU: &specs.LinuxCPU{Shares: &shares},
		}); err != nil {
			t.Fatalf("UpdateRuntime: %v", err)
		}
	})

	t.Run("AddProcAndPidsAndProcs", func(t *testing.T) {
		pid := os.Getpid()
		if err := cgr.AddProc(uint64(pid)); err != nil {
			t.Fatalf("AddProc(%d): %v", pid, err)
		}
		processes, err := cgr.Pids(runtimePath)
		if err != nil {
			t.Fatalf("Pids: %v", err)
		}
		procs, err := cgr.Procs(runtimePath)
		if err != nil {
			t.Fatalf("Procs: %v", err)
		}
		if !slices.Contains(procs, int32(pid)) {
			t.Fatalf("Procs need to contain %v", pid)
		}
		if len(processes) == 0 {
			t.Errorf("Pids: got empty process list, want at least one pid")
		}
	})

	t.Run("CpuUsage", func(t *testing.T) {
		usage, err := cgr.CpuUsage(runtimePath)
		if err != nil {
			t.Fatalf("CpuUsage: %v", err)
		}
		if usage == nil {
			t.Errorf("CpuUsage: got nil usage")
		}
	})

	t.Run("CpuStatRaw", func(t *testing.T) {
		raw, err := cgr.CpuStatRaw(runtimePath)
		if err != nil {
			t.Fatalf("CpuStatRaw: %v", err)
		}
		if raw == nil {
			t.Errorf("CpuStatRaw: got nil map")
		}
	})

	t.Run("CpuQuotaAndPeriod", func(t *testing.T) {
		quota, err := cgr.CpuQuotaAndPeriod(runtimePath)
		if err != nil {
			t.Fatalf("CpuQuotaAndPeriod (no limit): %v", err)
		}
		if quota == nil || quota.Quota != math.MaxUint64 {
			t.Errorf("CpuQuotaAndPeriod (no limit): got %v, want math.MaxUint64", quota)
		}

		const wantQuota uint64 = 50000
		const wantPeriod uint64 = 100000
		period, q := wantPeriod, int64(wantQuota)

		if err = cgr.UpdateRuntime(&specs.LinuxResources{
			CPU: &specs.LinuxCPU{Period: &period, Quota: &q},
		}); err != nil {
			t.Fatalf("UpdateRuntime (set quota): %v", err)
		}

		got, err := cgr.CpuQuotaAndPeriod(runtimePath)
		if err != nil {
			t.Fatalf("CpuQuotaAndPeriod (with limit): %v", err)
		}
		if got == nil {
			t.Fatalf("CpuQuotaAndPeriod (with limit): got nil")
		}
		if got.Quota != wantQuota {
			t.Errorf("CpuQuotaAndPeriod.Quota: got %d, want %d", got.Quota, wantQuota)
		}
		if got.Period != wantPeriod {
			t.Errorf("CpuQuotaAndPeriod.Period: got %d, want %d", got.Period, wantPeriod)
		}
	})

	t.Run("MemoryStatRaw", func(t *testing.T) {
		raw, err := cgr.MemoryStatRaw(runtimePath)
		if err != nil {
			t.Fatalf("MemoryStatRaw: %v", err)
		}
		if raw == nil {
			t.Errorf("MemoryStatRaw: got nil map")
		}
	})

	t.Run("MemoryEventRaw", func(t *testing.T) {
		_, err := cgr.MemoryEventRaw(runtimePath)
		if err != nil {
			t.Fatalf("MemoryEventRaw: %v", err)
		}
	})

	t.Run("MemoryUsage", func(t *testing.T) {
		usage, err := cgr.MemoryUsage(runtimePath)
		if err != nil {
			t.Fatalf("MemoryUsage: %v", err)
		}
		if usage == nil {
			t.Errorf("MemoryUsage: got nil usage")
		}
	})
}

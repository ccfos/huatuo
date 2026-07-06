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

package v2

import (
	"testing"

	"github.com/godbus/dbus/v5"
	"github.com/opencontainers/runtime-spec/specs-go"
)

// The tests below cover the fix in commit 0b86d742: on cgroup v2 with the
// systemd driver, resource updates must go through systemd D-Bus properties
// (CPUQuotaPerSecUSec, MemoryMax) instead of writing directly to cgroupfs,
// otherwise systemd overwrites them on reload.
//
// specToSystemdProperties is the pure translation step from OCI spec to
// systemd unit properties. It is the correctness core of the fix and is
// unit-testable without root or a running systemd.

func i64ptr(v int64) *int64   { return &v }
func u64ptr(v uint64) *uint64 { return &v }

// findProperty returns the dbus.Variant value for the named systemd property,
// or nil if absent. Property order is not part of the contract.
func findProperty(t *testing.T, props []struct {
	Name  string
	Value dbus.Variant
}, name string,
) *dbus.Variant {
	t.Helper()
	for i := range props {
		if props[i].Name == name {
			return &props[i].Value
		}
	}
	return nil
}

func TestSpecToSystemdProperties(t *testing.T) {
	tests := []struct {
		name string
		spec *specs.LinuxResources
		// wantNames lists property names that MUST be present, in any order.
		wantNames []string
		// wantValues maps property name -> expected uint64 value.
		wantValues map[string]uint64
	}{
		{
			// Baseline: 1 core (quota=100000, period=100000) -> 1_000_000 usec/sec.
			// Exact multiple of 10000, so the rounding branch is not taken.
			name: "cpu-one-core-exact",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Quota:  i64ptr(100000),
					Period: u64ptr(100000),
				},
			},
			wantNames:  []string{"CPUQuotaPerSecUSec"},
			wantValues: map[string]uint64{"CPUQuotaPerSecUSec": 1000000},
		},
		{
			// 0.5 core -> 500000 usec/sec, also an exact 10000 multiple.
			name: "cpu-half-core-exact",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Quota:  i64ptr(50000),
					Period: u64ptr(100000),
				},
			},
			wantValues: map[string]uint64{"CPUQuotaPerSecUSec": 500000},
		},
		{
			// quota=1, period=100000 -> 10 usec/sec, not a 10000 multiple,
			// so the code must round UP to 10000. This guards the branch
			//   cpuQuotaPerSecUSec = ((cpuQuotaPerSecUSec / 10000) + 1) * 10000
			// which is required by systemd's granularity.
			name: "cpu-round-up-tiny-quota",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Quota:  i64ptr(1),
					Period: u64ptr(100000),
				},
			},
			wantValues: map[string]uint64{"CPUQuotaPerSecUSec": 10000},
		},
		{
			// 12345 usec/period, period=100000 -> 123450 usec/sec, must round
			// up to 130000 (not 120000 — the fix always rounds up when the
			// remainder is non-zero, matching systemd behavior).
			name: "cpu-round-up-non-multiple",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Quota:  i64ptr(12345),
					Period: u64ptr(100000),
				},
			},
			wantValues: map[string]uint64{"CPUQuotaPerSecUSec": 130000},
		},
		{
			// CPU section present but Quota <= 0 must be ignored: systemd
			// treats a zero/negative quota as "unlimited" which would silently
			// erase an existing limit. The fix guards *Quota > 0.
			name: "cpu-zero-quota-skipped",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Quota:  i64ptr(0),
					Period: u64ptr(100000),
				},
			},
			wantNames: nil,
		},
		{
			name: "cpu-negative-quota-skipped",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Quota:  i64ptr(-1),
					Period: u64ptr(100000),
				},
			},
			wantNames: nil,
		},
		{
			// Period=0 would divide-by-zero; must be skipped.
			name: "cpu-zero-period-skipped",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Quota:  i64ptr(50000),
					Period: u64ptr(0),
				},
			},
			wantNames: nil,
		},
		{
			// Nil pointers on optional sub-fields must be tolerated.
			name: "cpu-nil-quota-skipped",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{Period: u64ptr(100000)},
			},
			wantNames: nil,
		},
		{
			name: "cpu-nil-period-skipped",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{Quota: i64ptr(50000)},
			},
			wantNames: nil,
		},
		{
			// Memory limit maps to MemoryMax (uint64). Signed->unsigned cast
			// mirrors the source.
			name: "memory-only-1gib",
			spec: &specs.LinuxResources{
				Memory: &specs.LinuxMemory{Limit: i64ptr(1 << 30)},
			},
			wantValues: map[string]uint64{"MemoryMax": 1 << 30},
		},
		{
			// Both CPU and Memory populated -> two properties.
			name: "cpu-and-memory",
			spec: &specs.LinuxResources{
				CPU: &specs.LinuxCPU{
					Quota:  i64ptr(200000),
					Period: u64ptr(100000),
				},
				Memory: &specs.LinuxMemory{Limit: i64ptr(512 * 1024 * 1024)},
			},
			wantValues: map[string]uint64{
				"CPUQuotaPerSecUSec": 2000000,
				"MemoryMax":          512 * 1024 * 1024,
			},
		},
		{
			// Memory section present but Limit nil must not emit MemoryMax.
			name: "memory-nil-limit-skipped",
			spec: &specs.LinuxResources{
				Memory: &specs.LinuxMemory{},
			},
			wantNames: nil,
		},
		{
			// Empty spec -> no properties. This is what makes UpdateRuntime
			// safe to skip the D-Bus call entirely.
			name:      "empty-spec",
			spec:      &specs.LinuxResources{},
			wantNames: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := specToSystemdProperties(tc.spec)

			// Normalise to a name/value list independent of the concrete
			// systemdDbus.Property struct so this test does not import the
			// systemd package just for reflection.
			normalised := make([]struct {
				Name  string
				Value dbus.Variant
			}, len(got))
			for i, p := range got {
				normalised[i].Name = p.Name
				normalised[i].Value = p.Value
			}

			// Combine expected names: explicit wantNames plus every key in
			// wantValues.
			want := map[string]bool{}
			for _, n := range tc.wantNames {
				want[n] = true
			}
			for n := range tc.wantValues {
				want[n] = true
			}

			if len(normalised) != len(want) {
				t.Fatalf("property count: got %d %v, want %d %v",
					len(normalised), propNames(normalised), len(want), keys(want))
			}

			for name, wantVal := range tc.wantValues {
				v := findProperty(t, normalised, name)
				if v == nil {
					t.Fatalf("property %q: missing, got %v", name, propNames(normalised))
				}
				var gotVal uint64
				if err := v.Store(&gotVal); err != nil {
					t.Fatalf("property %q: decode variant: %v", name, err)
				}
				if gotVal != wantVal {
					t.Errorf("property %q: got %d, want %d", name, gotVal, wantVal)
				}
			}
		})
	}
}

func propNames(props []struct {
	Name  string
	Value dbus.Variant
},
) []string {
	names := make([]string, len(props))
	for i, p := range props {
		names[i] = p.Name
	}
	return names
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestUpdateRuntimeEmptySpecNoDbusCall verifies the short-circuit in
// UpdateRuntime: when the spec produces no properties, the function must
// return nil without attempting to open a systemd D-Bus connection. This
// matters because UpdateRuntime is called opportunistically and must not
// fail on hosts where systemd is absent (e.g. minimal test containers).
func TestUpdateRuntimeEmptySpecNoDbusCall(t *testing.T) {
	c := &CgroupV2{name: "unified", unitName: "does-not-matter.slice"}

	if err := c.UpdateRuntime(&specs.LinuxResources{}); err != nil {
		t.Fatalf("UpdateRuntime(empty): got %v, want nil (must short-circuit before D-Bus)", err)
	}

	// Memory section without Limit is also empty in property terms.
	if err := c.UpdateRuntime(&specs.LinuxResources{Memory: &specs.LinuxMemory{}}); err != nil {
		t.Fatalf("UpdateRuntime(empty memory): got %v, want nil", err)
	}

	// CPU section with invalid quota/period must also be a no-op.
	if err := c.UpdateRuntime(&specs.LinuxResources{
		CPU: &specs.LinuxCPU{Quota: i64ptr(0), Period: u64ptr(100000)},
	}); err != nil {
		t.Fatalf("UpdateRuntime(zero quota): got %v, want nil", err)
	}
}

// TestPostfixConstant pins the systemd unit suffix. The fix in NewRuntime
// stores `path + Postfix` as unitName; downstream D-Bus calls target that
// exact unit name, so a change here would silently break UpdateRuntime.
func TestPostfixConstant(t *testing.T) {
	if Postfix != ".slice" {
		t.Errorf("Postfix: got %q, want %q", Postfix, ".slice")
	}
}

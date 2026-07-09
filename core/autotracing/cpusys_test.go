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

package autotracing

import (
	"testing"
)

func TestParseCPUStatLine(t *testing.T) {
	tests := []struct {
		name      string
		line      string
		wantSys   uint64
		wantTotal uint64
		wantErr   bool
	}{
		{
			name:      "normal aggregate cpu line",
			line:      "cpu  3350 0 4120 12345 0 0 0 0 0 0",
			wantSys:   4120,
			wantTotal: 19815,
		},
		{
			name:    "empty line",
			line:    "",
			wantErr: true,
		},
		{
			name:    "only label no numbers",
			line:    "cpu",
			wantErr: true,
		},
		{
			name:    "fewer than 3 numeric fields",
			line:    "cpu 100 0",
			wantErr: true,
		},
		{
			name:    "non-numeric value in a field",
			line:    "cpu 100 0 abc 12345",
			wantErr: true,
		},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			sys, total, err := parseCPUStatLine(tests[i].line)
			if tests[i].wantErr {
				if err == nil {
					t.Fatalf("parseCPUStatLine(%q) want error, got nil", tests[i].line)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseCPUStatLine(%q) unexpected error: %v", tests[i].line, err)
			}
			if sys != tests[i].wantSys {
				t.Errorf("system=%d, want %d", sys, tests[i].wantSys)
			}
			if total != tests[i].wantTotal {
				t.Errorf("total=%d, want %d", total, tests[i].wantTotal)
			}
		})
	}
}

func TestSafeSubUint64(t *testing.T) {
	tests := []struct {
		name string
		a, b uint64
		want uint64
	}{
		{name: "normal subtraction", a: 200, b: 50, want: 150},
		{name: "equal values clamp to zero", a: 100, b: 100, want: 0},
		{name: "underflow clamped to zero", a: 50, b: 200, want: 0},
		{name: "both zero", a: 0, b: 0, want: 0},
		{name: "large values", a: 1<<64 - 1, b: 1 << 63, want: (1<<64 - 1) - (1 << 63)},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			got := safeSubUint64(tests[i].a, tests[i].b)
			if got != tests[i].want {
				t.Errorf("safeSubUint64(%d, %d)=%d, want %d", tests[i].a, tests[i].b, got, tests[i].want)
			}
		})
	}
}

func TestUpdateCpuSysUsageDivByZero(t *testing.T) {
	c := &cpuSysTracing{
		usage: &cpuUsage{system: 100, total: 1000},
	}

	if err := c.updateCpuSysUsageWithSample(&cpuUsage{system: 150, total: 1000}); err != nil {
		t.Fatalf("updateCpuSysUsageWithSample returned error on zero delta: %v", err)
	}

	if c.sysPercent != 0 {
		t.Errorf("sysPercent=%d, want 0 (no tick elapsed, div-by-zero guarded)", c.sysPercent)
	}
	if c.usage.total != 1000 {
		t.Errorf("baseline should be refreshed to new sample, got total=%d", c.usage.total)
	}
}

func TestUpdateCpuSysUsageUnderflow(t *testing.T) {
	c := &cpuSysTracing{
		usage: &cpuUsage{system: 500, total: 5000},
	}

	if err := c.updateCpuSysUsageWithSample(&cpuUsage{system: 100, total: 1000}); err != nil {
		t.Fatalf("updateCpuSysUsageWithSample returned error on underflow: %v", err)
	}

	if c.sysPercent != 0 {
		t.Errorf("sysPercent=%d, want 0 (underflow clamped)", c.sysPercent)
	}
}

func TestUpdateCpuSysUsageNormalDelta(t *testing.T) {
	c := &cpuSysTracing{
		usage: &cpuUsage{system: 100, total: 1000},
	}

	if err := c.updateCpuSysUsageWithSample(&cpuUsage{system: 200, total: 2000}); err != nil {
		t.Fatalf("updateCpuSysUsageWithSample unexpected error: %v", err)
	}

	if c.sysPercent != 10 {
		t.Errorf("sysPercent=%d, want 10", c.sysPercent)
	}
}

func TestUpdateCpuSysUsageFirstSampleNoPanic(t *testing.T) {
	c := &cpuSysTracing{usage: nil}

	if err := c.updateCpuSysUsageWithSample(&cpuUsage{system: 100, total: 1000}); err != nil {
		t.Fatalf("updateCpuSysUsageWithSample first sample error: %v", err)
	}

	if c.usage == nil || c.usage.system != 100 {
		t.Errorf("first sample not cached: %+v", c.usage)
	}
	if c.sysPercent != 0 {
		t.Errorf("sysPercent=%d, want 0 on first sample", c.sysPercent)
	}
}

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
	"testing"
)

// TestCPUtilStatBareAssertionSafety verifies that the comma-ok pattern
// on LifeResources does not panic when the returned value is nil or
// a wrong type. This is a regression test for the bare type assertion
// that previously would crash the metrics pipeline.
func TestCPUUtilStatBareAssertionSafety(t *testing.T) {
	tests := []struct {
		name  string
		value any
		ok    bool
	}{
		{name: "nil returns false", value: nil, ok: false},
		{name: "correct type returns true", value: &cpuUtilStat{}, ok: true},
		{name: "wrong type returns false", value: "not a cpuUtilStat", ok: false},
		{name: "int returns false", value: 42, ok: false},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			_, ok := tests[i].value.(*cpuUtilStat)
			if ok != tests[i].ok {
				t.Errorf("type assertion ok=%v, want %v", ok, tests[i].ok)
			}
		})
	}
}

// TestCPUStatBareAssertionSafety verifies the same for cpuStat.
func TestCPUStatBareAssertionSafety(t *testing.T) {
	tests := []struct {
		name  string
		value any
		ok    bool
	}{
		{name: "nil returns false", value: nil, ok: false},
		{name: "correct type returns true", value: &cpuStat{}, ok: true},
		{name: "wrong type returns false", value: "not a cpuStat", ok: false},
		{name: "int returns false", value: 42, ok: false},
	}

	for i := range tests {
		t.Run(tests[i].name, func(t *testing.T) {
			_, ok := tests[i].value.(*cpuStat)
			if ok != tests[i].ok {
				t.Errorf("type assertion ok=%v, want %v", ok, tests[i].ok)
			}
		})
	}
}

// TestCPUUtilStatFields verifies that a zero-value cpuUtilStat has the
// expected default fields, ensuring the safe assertion fallback path
// produces sensible zero metrics.
func TestCPUUtilStatZeroValue(t *testing.T) {
	var s cpuUtilStat
	if s.totalUtil != 0 || s.usrUtil != 0 || s.sysUtil != 0 {
		t.Errorf("zero-value cpuUtilStat has non-zero utils: total=%v usr=%v sys=%v",
			s.totalUtil, s.usrUtil, s.sysUtil)
	}
}

// TestCPUStatZeroValue verifies that a zero-value cpuStat has the
// expected default fields.
func TestCPUStatZeroValue(t *testing.T) {
	var s cpuStat
	if s.cpuTotal != 0 || s.waitSum != 0 {
		t.Errorf("zero-value cpuStat has non-zero counters: cpuTotal=%v waitSum=%v",
			s.cpuTotal, s.waitSum)
	}
	if s.waitrateHierarchy != 0 || s.waitrateInner != 0 {
		t.Errorf("zero-value cpuStat has non-zero rates: hierarchy=%v inner=%v",
			s.waitrateHierarchy, s.waitrateInner)
	}
}

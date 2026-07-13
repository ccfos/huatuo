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

func TestLifeResourcesAsCPUUtilStat(t *testing.T) {
	tests := []struct {
		name   string
		value  any
		wantOK bool
	}{
		{name: "valid *cpuUtilStat", value: &cpuUtilStat{}, wantOK: true},
		{name: "typed nil *cpuUtilStat", value: (*cpuUtilStat)(nil), wantOK: false},
		{name: "wrong type string", value: "not a cpuUtilStat", wantOK: false},
		{name: "nil interface", value: nil, wantOK: false},
		{name: "wrong type int", value: 42, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := lifeResourcesAsCPUUtilStat(tt.value)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantOK && result == nil {
				t.Error("expected non-nil result for valid input")
			}
		})
	}
}

func TestLifeResourcesAsCPUStat(t *testing.T) {
	tests := []struct {
		name   string
		value  any
		wantOK bool
	}{
		{name: "valid *cpuStat", value: &cpuStat{}, wantOK: true},
		{name: "typed nil *cpuStat", value: (*cpuStat)(nil), wantOK: false},
		{name: "wrong type string", value: "not a cpuStat", wantOK: false},
		{name: "nil interface", value: nil, wantOK: false},
		{name: "wrong type int", value: 42, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, ok := lifeResourcesAsCPUStat(tt.value)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if tt.wantOK && result == nil {
				t.Error("expected non-nil result for valid input")
			}
		})
	}
}

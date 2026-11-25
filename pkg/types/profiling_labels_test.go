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

package types

import "testing"

func TestValidateNumericProfileLabels(t *testing.T) {
	for _, name := range []string{"pid", "cpu", "irq_vector", "lock_address"} {
		t.Run(name, func(t *testing.T) {
			valid := []string{"0", "1", "4294967295"}
			invalid := []string{"", "00", "01", "+1", "-1", " 1", "1 ", "1_0", "4294967296"}
			if name == "lock_address" {
				valid = []string{"0x0", "0x1", "0xabcdef", "0xffffffffffffffff"}
				invalid = []string{"", "0x", "0x00", "0x01", "0X1", "0xAB", "0x+1", "0x-1", "0x1_0", "0xg", "0x10000000000000000"}
			}
			for _, value := range valid {
				if err := ValidateLabels([]ProfileLabel{ProfileStringLabel(name, value)}); err != nil {
					t.Errorf("valid %s=%q: %v", name, value, err)
				}
			}
			for _, value := range invalid {
				if err := ValidateLabels([]ProfileLabel{ProfileStringLabel(name, value)}); err == nil {
					t.Errorf("accepted invalid %s=%q", name, value)
				}
			}
		})
	}
}

func TestValidateIRQLabels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		values []string
	}{
		{
			name:   "irq_context",
			values: []string{"source", "victim"},
		},
		{
			name:   "irq_type",
			values: []string{"hardirq", "softirq"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, value := range tc.values {
				if err := ValidateLabels([]ProfileLabel{ProfileStringLabel(tc.name, value)}); err != nil {
					t.Errorf("valid %s=%q: %v", tc.name, value, err)
				}
			}

			for _, value := range []string{"", "unknown", "SOURCE", "softirq ", "0"} {
				if err := ValidateLabels([]ProfileLabel{ProfileStringLabel(tc.name, value)}); err == nil {
					t.Errorf("accepted invalid %s=%q", tc.name, value)
				}
			}

			if err := ValidateLabels([]ProfileLabel{{
				Name: tc.name,
				Kind: ProfileLabelNumber,
			}}); err == nil {
				t.Error("accepted numeric enum label")
			}

			if _, err := NormalizeLabels([]ProfileLabel{
				ProfileStringLabel(tc.name, tc.values[0]),
				ProfileStringLabel(tc.name, tc.values[1]),
			}); err == nil {
				t.Error("accepted multiple enum values")
			}
		})
	}

	if _, err := NormalizeLabels([]ProfileLabel{
		ProfileStringLabel("irq_vector", "1"),
		ProfileStringLabel("irq_vector", "2"),
	}); err == nil {
		t.Error("accepted multiple IRQ vectors")
	}
}

func TestValidateOffCPUReason(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels []ProfileLabel
		valid  bool
	}{
		{
			name:   "blocked",
			labels: []ProfileLabel{ProfileStringLabel("offcpu_reason", "blocked")},
			valid:  true,
		},
		{
			name:   "unknown",
			labels: []ProfileLabel{ProfileStringLabel("offcpu_reason", "unknown")},
			valid:  true,
		},
		{
			name:   "empty",
			labels: []ProfileLabel{ProfileStringLabel("offcpu_reason", "")},
		},
		{
			name: "numeric",
			labels: []ProfileLabel{{
				Name:        "offcpu_reason",
				Kind:        ProfileLabelNumber,
				NumberValue: 1,
			}},
		},
		{
			name: "multiple reasons",
			labels: []ProfileLabel{
				ProfileStringLabel("offcpu_reason", "blocked"),
				ProfileStringLabel("offcpu_reason", "runqueue"),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateLabels(tc.labels)
			if (err == nil) != tc.valid {
				t.Fatalf("ValidateLabels() = %v, want valid=%t", err, tc.valid)
			}
		})
	}
}

func BenchmarkValidateNumericProfileLabels(b *testing.B) {
	for _, label := range []ProfileLabel{ProfilePIDLabel(12345), ProfileCPULabel(3), ProfileLockAddressLabel(0xffff888012345678)} {
		b.Run(label.Name, func(b *testing.B) {
			labels := []ProfileLabel{label}
			b.ReportAllocs()
			for b.Loop() {
				if err := ValidateLabels(labels); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

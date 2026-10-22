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

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
)

func ProfileStringLabel(name, value string) ProfileLabel {
	return ProfileLabel{
		Name:        name,
		Kind:        ProfileLabelString,
		StringValue: value,
	}
}

func ProfilePIDLabel(pid uint32) ProfileLabel {
	return ProfileStringLabel("pid", strconv.FormatUint(uint64(pid), 10))
}

func ProfileCPULabel(cpu uint32) ProfileLabel {
	return ProfileStringLabel("cpu", strconv.FormatUint(uint64(cpu), 10))
}

func ProfileLockAddressLabel(address uint64) ProfileLabel {
	return ProfileStringLabel("lock_address", "0x"+strconv.FormatUint(address, 16))
}

func compareProfileLabels(a, b ProfileLabel) int {
	if n := cmp.Compare(a.Name, b.Name); n != 0 {
		return n
	}

	if n := cmp.Compare(a.Kind, b.Kind); n != 0 {
		return n
	}

	if n := cmp.Compare(a.StringValue, b.StringValue); n != 0 {
		return n
	}

	if n := cmp.Compare(a.NumberValue, b.NumberValue); n != 0 {
		return n
	}

	return cmp.Compare(a.NumberUnit, b.NumberUnit)
}

// NormalizeLabels mutates exclusively owned input, including on failure.
// Callers must use the returned slice and must not submit rejected input.
func NormalizeLabels(labels []ProfileLabel) ([]ProfileLabel, error) {
	slices.SortFunc(labels, compareProfileLabels)
	labels = slices.Compact(labels)
	if err := ValidateLabels(labels); err != nil {
		return nil, err
	}

	return labels, nil
}

// ValidateLabels checks canonical order without repairing borrowed input.
func ValidateLabels(labels []ProfileLabel) error {
	for i := range labels {
		label := &labels[i]
		if err := validateProfileLabel(label); err != nil {
			return err
		}

		if i == 0 {
			continue
		}

		previous := &labels[i-1]
		if compareProfileLabels(*previous, *label) >= 0 {
			return fmt.Errorf("profiling labels must be strictly sorted and deduplicated: %q", label.Name)
		}

		if previous.Name == label.Name && singleProfileLabel(label.Name) {
			return fmt.Errorf("profiling label %q permits only one value", label.Name)
		}
	}

	return nil
}

func singleProfileLabel(name string) bool {
	switch name {
	case "pid", "cpu", "comm", "lock_address", "irq", "softirq_vector", "offcpu_category":
		return true
	default:
		return false
	}
}

func validateProfileLabel(label *ProfileLabel) error {
	if label.Name == "" {
		return fmt.Errorf("profiling label name is required")
	}

	switch label.Kind {
	case ProfileLabelString:
		if label.NumberValue != 0 || label.NumberUnit != "" {
			return fmt.Errorf("string label %q contains numeric fields", label.Name)
		}
	case ProfileLabelNumber:
		if label.StringValue != "" {
			return fmt.Errorf("numeric label %q contains a string value", label.Name)
		}
	default:
		return fmt.Errorf("profiling label %q has invalid kind %d", label.Name, label.Kind)
	}

	if !singleProfileLabel(label.Name) {
		return nil
	}

	if label.Kind != ProfileLabelString {
		return fmt.Errorf("built-in label %q requires a string", label.Name)
	}

	value := label.StringValue
	var digits [20]byte // Enough for a uint64 in decimal or hexadecimal without heap allocation.
	switch label.Name {
	case "pid", "cpu", "softirq_vector":
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil || string(strconv.AppendUint(digits[:0], n, 10)) != value {
			return fmt.Errorf("label %q requires a canonical unsigned decimal: %q", label.Name, value)
		}
	case "lock_address":
		if len(value) < 3 || value[:2] != "0x" {
			return fmt.Errorf("lock_address requires canonical 0x-prefixed hexadecimal: %q", value)
		}

		n, err := strconv.ParseUint(value[2:], 16, 64)
		if err != nil || string(strconv.AppendUint(digits[:0], n, 16)) != value[2:] {
			return fmt.Errorf("lock_address requires canonical hexadecimal: %q", value)
		}
	case "irq":
		if value != "source" && value != "victim" {
			return fmt.Errorf("irq requires source or victim: %q", value)
		}
	case "offcpu_category":
		if value == "" {
			return fmt.Errorf("offcpu_category must not be empty")
		}
	}

	return nil
}

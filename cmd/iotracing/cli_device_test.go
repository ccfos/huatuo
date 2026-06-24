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

package main

import "testing"

func TestParseDeviceNumbersRejectsInvalidSpecs(t *testing.T) {
	cases := []string{
		"",
		" ",
		",",
		"4096:0",
		"8:1048576",
		"8",
		"8:0:1",
	}

	for _, tc := range cases {
		t.Run(tc, func(t *testing.T) {
			if _, err := parseDeviceNumbers(tc); err == nil {
				t.Fatalf("parseDeviceNumbers(%q) error = nil, want error", tc)
			}
		})
	}
}

func TestParseDeviceNumbersAcceptsBoundaryValues(t *testing.T) {
	got, err := parseDeviceNumbers("8:0,4095:1048575")
	if err != nil {
		t.Fatalf("parseDeviceNumbers() error = %v", err)
	}

	want := []uint32{
		8 << 20,
		4095<<20 | 1048575,
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("device[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

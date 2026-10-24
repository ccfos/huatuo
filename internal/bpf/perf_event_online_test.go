// Copyright 2025, 2026 The HuaTuo Authors
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

// A simple link type implemented by referring to the Cilium community.
// link/perf_event.go

package bpf

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOnlinePerfCPUs(t *testing.T) {
	for _, tc := range []struct {
		name string
		list string
		want []int
	}{
		{"single", "4\n", []int{4}},
		{"contiguous", "0-3", []int{0, 1, 2, 3}},
		{"sparse", "0,2-3,8\n", []int{0, 2, 3, 8}},
		{"empty", "", nil},
		{"reversed", "3-1", nil},
		{"negative", "-1", nil},
		{"overlapping", "0-2,2-3", nil},
		{"unordered", "4,2", nil},
		{"trailing separator", "0,", nil},
		{"malformed", "0-a", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "online")
			if err := os.WriteFile(path, []byte(tc.list), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := onlinePerfCPUs(path)
			if tc.want == nil {
				if err == nil {
					t.Errorf("onlinePerfCPUs(%q) accepted invalid CPU list", tc.list)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("CPU IDs = %v, want %v", got, tc.want)
			}
		})
	}
	if _, err := onlinePerfCPUs(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing sysfs file accepted")
	}
}

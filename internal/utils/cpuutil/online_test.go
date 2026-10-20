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

package cpuutil

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOnlineCPUMaskRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "online")
	for _, tc := range []struct {
		list string
		want []bool
	}{
		{"0-1\n", []bool{true, true, false, false, false, false, false, false}},
		{"0-1,4-7\n", []bool{true, true, false, false, true, true, true, true}},
		{"0,7\n", []bool{true, false, false, false, false, false, false, true}},
		{"7,0,0\n", []bool{true, false, false, false, false, false, false, true}},
	} {
		if err := os.WriteFile(path, []byte(tc.list), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := OnlineCPUMask(path, 8)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("mask for %q = %v, want %v", tc.list, got, tc.want)
		}
	}
}

func TestOnlineCPUMaskRejectsInvalidTopology(t *testing.T) {
	path := filepath.Join(t.TempDir(), "online")
	for _, list := range []string{"", " \n", "-1", "3-1", "8", "0-8", "0,", "0,,1", "x", "0-x", "0-99999999999999999999999"} {
		t.Run(list, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(list), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OnlineCPUMask(path, 8); err == nil {
				t.Fatalf("accepted invalid CPU list %q", list)
			}
		})
	}
	if _, err := OnlineCPUMask(path, 0); err == nil {
		t.Fatal("accepted zero possible CPUs")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := OnlineCPUMask(path, 8); err == nil {
		t.Fatal("accepted missing online file")
	}
}

func BenchmarkOnlineCPUMask(b *testing.B) {
	path := filepath.Join(b.TempDir(), "online")
	if err := os.WriteFile(path, []byte("0-63,128-255\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := OnlineCPUMask(path, 256); err != nil {
			b.Fatal(err)
		}
	}
}

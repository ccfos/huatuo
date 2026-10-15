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

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadUsage(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  usage
		valid bool
	}{
		{"v1", map[string]string{"cpuacct.stat": "user 150\nsystem 50\n", "cpuacct.usage": "2000000000\n"}, usage{User: 1.5, System: .5, Total: 2}, true},
		{"v2", map[string]string{"cpu.stat": "usage_usec 2000000\nuser_usec 1500000\nsystem_usec 500000\nnr_throttled 0\n"}, usage{User: 1.5, System: .5, Total: 2}, true},
		{"missing v1 system", map[string]string{"cpuacct.stat": "user 150\n", "cpuacct.usage": "2000000000\n"}, usage{}, false},
		{"missing v2 total", map[string]string{"cpu.stat": "user_usec 1500000\nsystem_usec 500000\n"}, usage{}, false},
		{"invalid number", map[string]string{"cpu.stat": "usage_usec invalid\n"}, usage{}, false},
		{"missing file", nil, usage{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, data := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := readUsage(dir, 100)
			if (err == nil) != tc.valid || got != tc.want {
				t.Fatalf("readUsage() = %+v, %v; want %+v, valid=%t", got, err, tc.want, tc.valid)
			}
		})
	}
}

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

package profiler

import "testing"

func TestPythonThreadName(t *testing.T) {
	tests := []struct {
		name    string
		cmdline []string
		want    string
	}{
		{
			name:    "script after warning option",
			cmdline: []string{"python3", "-W", "ignore", "worker.py"},
			want:    "worker.py",
		},
		{
			name:    "script after implementation option",
			cmdline: []string{"python3", "-X", "dev", "worker.py"},
			want:    "worker.py",
		},
		{
			name:    "module",
			cmdline: []string{"python3", "-m", "http.server"},
			want:    "http.server",
		},
		{
			name:    "command",
			cmdline: []string{"python3", "-c", "print('ready')"},
			want:    "print('ready')",
		},
		{
			name:    "attached options",
			cmdline: []string{"python3", "-Wignore", "-Xdev", "worker.py"},
			want:    "worker.py",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := pythonThreadName(test.cmdline); got != test.want {
				t.Fatalf("pythonThreadName() = %q, want %q", got, test.want)
			}
		})
	}
}

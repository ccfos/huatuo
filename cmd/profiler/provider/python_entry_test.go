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

package provider

import "testing"

func TestPythonEntryName(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"stdin", []string{"/usr/bin/python3"}, "python3"},
		{"explicit stdin", []string{"python3", "-"}, "python3"},
		{"unbuffered stdin", []string{"python3", "-u"}, "python3"},
		{"interactive", []string{"python3", "-i"}, "python3"},
		{"script", []string{"python3", "app.py"}, "app.py"},
		{"unbuffered script", []string{"python3", "-u", "app.py"}, "app.py"},
		{"module", []string{"python3", "-m", "http.server"}, "http.server"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := pythonEntryName(tc.args); got != tc.want {
				t.Fatalf("entry = %q, want %q", got, tc.want)
			}
		})
	}
}

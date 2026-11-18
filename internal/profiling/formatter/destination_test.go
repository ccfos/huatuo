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

package formatter

import "testing"

func TestValidateDestination(t *testing.T) {
	for _, path := range []string{"", " "} {
		t.Run(path, func(t *testing.T) {
			if err := ValidateDestination(path); err == nil {
				t.Fatalf("accepted destination %q", path)
			}
		})
	}
	for _, path := range []string{"profile.svg", "profiles/", "/run/toolstream.sock", "stdout", "-", "/dev/stdout", "/dev/fd/1", "/proc/self/fd/1"} {
		if err := ValidateDestination(path); err != nil {
			t.Fatal(err)
		}
	}
}

func TestValidateTUIDestination(t *testing.T) {
	for _, tc := range []struct {
		name    string
		path    string
		wantErr bool
	}{
		{
			name: "omitted path",
		},
		{
			name: "stdout",
			path: "stdout",
		},
		{
			name: "stdout alias",
			path: "-",
		},
		{
			name:    "file path",
			path:    "/tmp/result",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateTUIDestination(tc.path); (err != nil) != tc.wantErr {
				t.Fatalf("ValidateTUIDestination(%q) = %v, want error: %t", tc.path, err, tc.wantErr)
			}
		})
	}
}

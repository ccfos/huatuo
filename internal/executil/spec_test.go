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

package executil

import (
	"strings"
	"testing"
	"time"
)

func TestNewValidatesAndSnapshotsSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    Spec
		wantErr string
	}{
		{
			name:    "missing path",
			spec:    Spec{},
			wantErr: "path must not be empty",
		},
		{
			name:    "blank path",
			spec:    Spec{Path: "  \t"},
			wantErr: "path must not be empty",
		},
		{
			name:    "null byte in path",
			spec:    Spec{Path: "bad\x00path"},
			wantErr: "path \"bad\\x00path\" contains a null byte",
		},
		{
			name:    "null byte in argument",
			spec:    Spec{Path: "/bin/true", Args: []string{"bad\x00arg"}},
			wantErr: "argument 0 contains a null byte",
		},
		{
			name:    "null byte in environment",
			spec:    Spec{Path: "/bin/true", Env: []string{"KEY=bad\x00value"}},
			wantErr: "environment entry 0 contains a null byte",
		},
		{
			name:    "invalid environment",
			spec:    Spec{Path: "/bin/true", Env: []string{"INVALID"}},
			wantErr: "environment entry 0 must use a non-empty KEY=VALUE form",
		},
		{
			name:    "empty environment key",
			spec:    Spec{Path: "/bin/true", Env: []string{"=value"}},
			wantErr: "environment entry 0 must use a non-empty KEY=VALUE form",
		},
		{
			name:    "negative stop grace period",
			spec:    Spec{Path: "/bin/true", StopGracePeriod: -time.Second},
			wantErr: "stop grace period must not be negative",
		},
		{
			name:    "negative output limit",
			spec:    Spec{Path: "/bin/true", MaxOutputBytes: -1},
			wantErr: "maximum output bytes must not be negative",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New(test.spec)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Errorf("New() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}

	args := []string{"first"}
	env := []string{"KEY=value"}
	process, err := New(Spec{Path: "/bin/true", Args: args, Env: env})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	args[0] = "mutated"
	env[0] = "KEY=mutated"
	if process.spec.Args[0] != "first" {
		t.Errorf("snapshotted argument = %q, want first", process.spec.Args[0])
	}

	if process.spec.Env[0] != "KEY=value" {
		t.Errorf("snapshotted environment = %q, want KEY=value", process.spec.Env[0])
	}

	if process.spec.StopGracePeriod != defaultStopGracePeriod {
		t.Errorf("StopGracePeriod = %s, want %s", process.spec.StopGracePeriod, defaultStopGracePeriod)
	}

	if process.spec.MaxOutputBytes != defaultMaxOutputBytes {
		t.Errorf("MaxOutputBytes = %d, want %d", process.spec.MaxOutputBytes, defaultMaxOutputBytes)
	}

	emptyEnvProcess, err := New(Spec{Path: "/bin/true", Env: []string{}})
	if err != nil {
		t.Fatalf("New() with empty environment error = %v", err)
	}

	if emptyEnvProcess.spec.Env == nil {
		t.Error("New() changed an explicit empty environment to inherited environment")
	}
}

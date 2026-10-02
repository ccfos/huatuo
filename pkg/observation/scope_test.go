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

package observation

import "testing"

func TestParseScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		want    Scope
		wantErr string
	}{
		{name: "host", value: "host", want: ScopeHost},
		{name: "container", value: "container", want: ScopeContainer},
		{
			name:    "empty",
			want:    ScopeUnknown,
			wantErr: `unsupported observation scope ""`,
		},
		{
			name:    "unknown",
			value:   "process",
			want:    ScopeUnknown,
			wantErr: `unsupported observation scope "process"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseScope(tt.value)
			if got != tt.want {
				t.Errorf("ParseScope(%q) = %q, want %q", tt.value, got, tt.want)
			}
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ParseScope(%q) error = %v, want nil", tt.value, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ParseScope(%q) error = nil, want %q", tt.value, tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Errorf("ParseScope(%q) error = %q, want %q", tt.value, err, tt.wantErr)
			}
		})
	}
}

func TestValidateScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		scope       Scope
		containerID string
		wantErr     string
	}{
		{name: "host", scope: ScopeHost},
		{name: "container", scope: ScopeContainer, containerID: "container-2026"},
		{
			name:        "host with container",
			scope:       ScopeHost,
			containerID: "container-2026",
			wantErr:     "container id must be empty for host scope",
		},
		{
			name:    "container without id",
			scope:   ScopeContainer,
			wantErr: "container id is required for container scope",
		},
		{
			name:    "unknown scope",
			scope:   ScopeUnknown,
			wantErr: `unsupported observation scope ""`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateScope(tt.scope, tt.containerID)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateScope(%q, %q) error = %v, want nil", tt.scope, tt.containerID, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateScope(%q, %q) error = nil, want %q", tt.scope, tt.containerID, tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Errorf(
					"ValidateScope(%q, %q) error = %q, want %q",
					tt.scope,
					tt.containerID,
					err,
					tt.wantErr,
				)
			}
		})
	}
}

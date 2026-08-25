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

package trace

import (
	"strings"
	"testing"

	"huatuo-bamai/internal/auth"
	"huatuo-bamai/pkg/observation"
	tracingdomain "huatuo-bamai/pkg/tracing"
)

func TestValidateCreateInput(t *testing.T) {
	valid := CreateInput{
		Hostname:        "node-1",
		DurationSeconds: 60,
		Scope:           observation.ScopeHost,
		Spec:            tracingdomain.Spec{Type: tracingdomain.TypeNetworkingDrop},
	}
	tests := []struct {
		name      string
		principal auth.Principal
		mutate    func(*CreateInput)
		wantErr   string
	}{
		{name: "valid", principal: auth.Principal{ID: "user-1"}},
		{name: "missing principal", wantErr: "authenticated user ID"},
		{
			name:      "missing duration",
			principal: auth.Principal{ID: "user-1"},
			mutate: func(input *CreateInput) {
				input.DurationSeconds = 0
			},
			wantErr: "duration_seconds",
		},
		{
			name:      "unsupported container scope",
			principal: auth.Principal{ID: "user-1"},
			mutate: func(input *CreateInput) {
				input.Scope = observation.ScopeContainer
				input.ContainerID = "container-1"
			},
			wantErr: "does not support scope",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := valid
			if tt.mutate != nil {
				tt.mutate(&input)
			}
			err := validateCreateInput(tt.principal, input)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("validateCreateInput() error = %v", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Fatalf("validateCreateInput() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestCapabilitiesRemainStatic(t *testing.T) {
	service := &Service{}
	first := service.Capabilities()
	if len(first) != 3 {
		t.Fatalf("Capabilities() count = %d, want 3", len(first))
	}
	first[0].SupportedScopes[0] = observation.ScopeContainer
	second := service.Capabilities()
	if second[0].SupportedScopes[0] != observation.ScopeHost {
		t.Fatalf("Capabilities() retained caller mutation: %+v", second[0])
	}
}

func TestNormalizePage(t *testing.T) {
	limit, offset := NormalizePage(nil, nil)
	if limit != 100 || offset != 0 {
		t.Fatalf("NormalizePage(nil, nil) = (%d, %d)", limit, offset)
	}
	customLimit, customOffset := 25, 50
	limit, offset = NormalizePage(&customLimit, &customOffset)
	if limit != customLimit || offset != customOffset {
		t.Fatalf("NormalizePage(custom) = (%d, %d)", limit, offset)
	}
}

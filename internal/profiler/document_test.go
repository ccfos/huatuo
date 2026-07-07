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

import (
	"testing"

	"huatuo-bamai/pkg/tracing"
)

func TestExtractProfilingMetadataProfileType(t *testing.T) {
	tests := []struct {
		name       string
		tracerData any
		want       string
	}{
		{
			name:       "nil data",
			tracerData: nil,
			want:       "",
		},
		{
			name: "valid profile type",
			tracerData: map[string]any{
				"flamedata": map[string]any{
					"profile_type": "cpu",
				},
			},
			want: "cpu",
		},
		{
			name:       "missing flamedata",
			tracerData: map[string]any{"other": "value"},
			want:       "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractProfilingMetadataProfileType(tt.tracerData)
			if got != tt.want {
				t.Errorf("extractProfilingMetadataProfileType() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProfilingDocumentMapperID(t *testing.T) {
	m := ProfilingDocumentMapper{}
	doc := &tracing.Document{TracerID: "test-id-123"}
	if got := m.ID(doc); got != "test-id-123" {
		t.Errorf("ID() = %q, want %q", got, "test-id-123")
	}
}

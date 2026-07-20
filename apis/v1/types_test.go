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

package v1

import (
	"encoding/json"
	"testing"
)

func TestCreateJobRequestJSONFields(t *testing.T) {
	tests := []struct {
		name    string
		request any
		fields  []string
	}{
		{
			name: "profiling job",
			request: CreateProfilingJobRequest{
				ProfilingType:   "cpu",
				BinaryMatchPath: "/usr/bin/example",
				Language:        "go",
				ContainerID:     "container-id",
			},
			fields: []string{
				"type",
				"binary_match_path",
				"language",
				"memory_mode",
				"duration",
				"container_id",
				"hostname",
			},
		},
		{
			name:    "trace job",
			request: CreateTraceJobRequest{},
			fields:  []string{"type", "duration", "container", "hostname"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := json.Marshal(tt.request)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}

			var decoded map[string]any
			if err := json.Unmarshal(payload, &decoded); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			if len(decoded) != len(tt.fields) {
				t.Fatalf("JSON field count = %d, want %d", len(decoded), len(tt.fields))
			}
			for _, field := range tt.fields {
				if _, ok := decoded[field]; !ok {
					t.Errorf("JSON field %q is missing", field)
				}
			}
		})
	}
}

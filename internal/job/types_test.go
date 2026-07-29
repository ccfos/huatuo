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

package job

import (
	"encoding/json"
	"testing"
)

func TestStandardizedJobJSONFields(t *testing.T) {
	tests := []struct {
		name   string
		value  any
		fields []string
	}{
		{
			name:   "agent task request",
			value:  AgentTaskRequest{},
			fields: []string{"tracer_args"},
		},
		{
			name:  "job",
			value: Job{ErrorMessage: "failed"},
			fields: []string{
				"id",
				"username",
				"container_id",
				"hostname",
				"agent_task_id",
				"error_message",
				"trace_timeout",
				"agent_task",
				"result",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatalf("json.Marshal() error=%v", err)
			}

			var decoded map[string]any
			if err := json.Unmarshal(payload, &decoded); err != nil {
				t.Fatalf("json.Unmarshal() error=%v", err)
			}
			for _, field := range tt.fields {
				if _, ok := decoded[field]; !ok {
					t.Errorf("JSON field %q is missing", field)
				}
			}
		})
	}
}

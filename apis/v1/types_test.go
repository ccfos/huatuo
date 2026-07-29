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
	"time"
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
				"duration_seconds",
				"container_id",
				"hostname",
			},
		},
		{
			name:    "trace job",
			request: CreateTraceJobRequest{},
			fields:  []string{"type", "duration_seconds", "container_id", "hostname"},
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

func TestProfilingCapabilitiesJSONFields(t *testing.T) {
	payload, err := json.Marshal(ProfilingCapabilities{})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	fields := []string{
		"types",
		"cpu_languages",
		"memory_languages",
		"memory_modes",
		"aggregation_interval_seconds",
		"max_concurrent_profilers",
	}
	if len(decoded) != len(fields) {
		t.Fatalf("JSON field count = %d, want %d", len(decoded), len(fields))
	}
	for _, field := range fields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("JSON field %q is missing", field)
		}
	}
}

func TestStandardizedJobJSONFields(t *testing.T) {
	tests := []struct {
		name   string
		value  any
		fields []string
	}{
		{
			name: "profiling job",
			value: ProfilingJob{
				ContainerID:     "container-2026",
				BinaryMatchPath: "/usr/bin/example",
			},
			fields: []string{
				"container_id",
				"binary_match_path",
				"language",
			},
		},
		{
			name: "trace job",
			value: TraceJob{
				ContainerID: "container-2026",
			},
			fields: []string{
				"container_id",
				"hostname",
				"duration_seconds",
				"created_at",
				"finished_at",
				"result_url",
				"status_reason",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := json.Marshal(tt.value)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}

			var decoded map[string]any
			if err := json.Unmarshal(payload, &decoded); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			for _, field := range tt.fields {
				if _, ok := decoded[field]; !ok {
					t.Errorf("JSON field %q is missing", field)
				}
			}
		})
	}
}

func TestProfilingJobJSONFields(t *testing.T) {
	payload, err := json.Marshal(ProfilingJob{
		ContainerID:     "container-2026",
		MemoryMode:      "object_alloc",
		BinaryMatchPath: "/usr/bin/example",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	fields := []string{
		"id",
		"container_id",
		"hostname",
		"type",
		"memory_mode",
		"language",
		"binary_match_path",
		"status",
		"duration_seconds",
		"created_at",
		"finished_at",
		"result_url",
		"status_reason",
	}
	if len(decoded) != len(fields) {
		t.Fatalf("JSON field count = %d, want %d: %s", len(decoded), len(fields), payload)
	}
	for _, field := range fields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("JSON field %q is missing", field)
		}
	}
}

func TestTraceJobJSONFields(t *testing.T) {
	payload, err := json.Marshal(TraceJob{
		ContainerID: "container-2026",
	})
	if err != nil {
		t.Fatalf("json.Marshal() error=%v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error=%v", err)
	}
	fields := []string{
		"id",
		"container_id",
		"hostname",
		"type",
		"status",
		"duration_seconds",
		"created_at",
		"finished_at",
		"result_url",
		"status_reason",
	}
	if len(decoded) != len(fields) {
		t.Fatalf("JSON field count=%d, want %d: %s", len(decoded), len(fields), payload)
	}
	for _, field := range fields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("JSON field %q is missing", field)
		}
	}
}

func TestRawProfileJSONFields(t *testing.T) {
	payload, err := json.Marshal(RawProfile{
		Hostname:          "host-2026",
		Region:            "integration",
		UploadedAt:        time.Date(2026, 7, 26, 10, 0, 1, 0, time.UTC),
		CapturedAt:        time.Date(2026, 7, 26, 10, 0, 0, 0, time.UTC),
		ContainerID:       "container-2026",
		ContainerHostname: "workload-2026",
		ContainerType:     "docker",
		ContainerQoS:      "burstable",
		ProfileType:       "process_cpu",
		Profile:           json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("json.Marshal() error=%v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error=%v", err)
	}
	fields := []string{
		"hostname",
		"region",
		"uploaded_at",
		"captured_at",
		"container_id",
		"container_hostname",
		"container_type",
		"container_qos",
		"profile_type",
		"profile",
	}
	if len(decoded) != len(fields) {
		t.Fatalf("JSON field count=%d, want %d: %s", len(decoded), len(fields), payload)
	}
	for _, field := range fields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("JSON field %q is missing", field)
		}
	}
}

func TestRawProfilePageJSONFields(t *testing.T) {
	payload, err := json.Marshal(RawProfilePage{
		Items: []RawProfile{},
	})
	if err != nil {
		t.Fatalf("json.Marshal() error=%v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error=%v", err)
	}
	fields := []string{"items", "limit", "offset", "has_more"}
	if len(decoded) != len(fields) {
		t.Fatalf("JSON field count=%d, want %d: %s", len(decoded), len(fields), payload)
	}
	for _, field := range fields {
		if _, ok := decoded[field]; !ok {
			t.Errorf("JSON field %q is missing", field)
		}
	}
}

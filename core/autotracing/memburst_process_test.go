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

package autotracing

import (
	"encoding/json"
	"reflect"
	"testing"

	tracingstore "github.com/ccfos/huatuo/pkg/tracing/store"
)

// memburst documents are persisted by encoding/json (pkg/tracing/store
// mapper.Encode), so the top_memory_usage records must keep the documented
// wire keys: pid / process_name / memory_size.
func TestMemburstTopMemoryUsageDocumentKeys(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(&tracingstore.Document{
		TracerData: &MemoryTracingData{
			TopMemoryUsage: []*processMemInfo{
				{PID: 3456, ProcessName: "java", MemSize: 8589934592},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal tracing document: %v", err)
	}

	var document struct {
		TracerData struct {
			TopMemoryUsage []map[string]any `json:"top_memory_usage"`
		} `json:"tracer_data"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("unmarshal tracing document: %v", err)
	}
	if len(document.TracerData.TopMemoryUsage) != 1 {
		t.Fatalf("top_memory_usage records = %d, want 1", len(document.TracerData.TopMemoryUsage))
	}

	want := map[string]any{
		"pid":          float64(3456),
		"process_name": "java",
		"memory_size":  float64(8589934592),
	}
	if got := document.TracerData.TopMemoryUsage[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("top_memory_usage[0] = %v, want %v", got, want)
	}
}

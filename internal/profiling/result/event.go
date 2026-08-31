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

// Package result defines the profiling result transferred over Toolstream.
package result

import (
	"time"

	profilingstore "huatuo-bamai/pkg/profiling/store"
)

// ToolName identifies profiling result streams.
const ToolName = "profiler"

// Event is one profiling aggregation window sent by the profiler subprocess.
type Event struct {
	TracerID         string                      `json:"tracer_id,omitempty"`
	ContainerID      string                      `json:"container_id,omitempty"`
	TracerName       string                      `json:"tracer_name,omitempty"`
	TracerRunType    string                      `json:"tracer_type,omitempty"`
	StartedTimestamp time.Time                   `json:"started_timestamp"`
	ProfileData      *profilingstore.ProfileData `json:"profile_data,omitempty"`
}

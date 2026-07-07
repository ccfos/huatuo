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

// StartProfilingRequest represents a request to start profiling
type StartProfilingRequest struct {
	Type                  string `json:"type"`                    // cpu or memory
	TargetExecPath        string `json:"target_exec_path"`        // executable path for CPU profiling
	TargetProcessLanguage string `json:"target_process_language"` // programming language of the target process
	MemoryMode            string `json:"memory_mode"`             // memory profiling mode
	Duration              int    `json:"duration"`                // profiling duration in seconds
	Container             string `json:"container"`               // container name or ID
	Hostname              string `json:"hostname"`                // host name
}

// StartProfilingResponse represents a response to start profiling
type StartProfilingResponse struct {
	ID string `json:"id"` // profiling task ID
}

// ProfilingStatusResponse represents a profiling status response
type ProfilingStatusResponse struct {
	ID                    string           `json:"id"`                      // profiling task ID
	AgentTaskID           string           `json:"agent_task_id"`           // agent task ID
	Container             string           `json:"container"`               // container name or ID
	Hostname              string           `json:"hostname"`                // host name
	Status                string           `json:"status"`                  // task status
	StartTime             string           `json:"start_time"`              // start time
	EndTime               string           `json:"end_time"`                // end time
	TracerArgs            []string         `json:"tracer_args"`             // tracer arguments
	Duration              int              `json:"duration"`                // profiling duration
	Results               ProfilingResults `json:"results"`                 // profiling results
	ErrorMessage          string           `json:"error_message"`           // error message if any
	Type                  string           `json:"type"`                    // cpu or memory
	TargetExecPath        string           `json:"target_exec_path"`        // executable path for CPU profiling
	TargetProcessLanguage string           `json:"target_process_language"` // programming language of the target process
	MemoryMode            string           `json:"memory_mode"`             // memory profiling mode
}

// ProfilingResults represents profiling results
type ProfilingResults struct {
	URL string `json:"url"` // URL to view the results
}

// RawDataResponse represents raw profiling data response
type RawDataResponse struct {
	Data any `json:"data"` // raw profiling data
}

// JobFilter represents a job filter
type JobFilter struct {
	Container string `json:"container"` // container name or ID
	Host      string `json:"host"`      // host name
	Status    string `json:"status"`    // job status
	Type      string `json:"type"`      // job type
}

// StartTraceRequest represents a request to start tracing
type StartTraceRequest struct {
	Type      string `json:"type"`      // trace type
	Duration  int    `json:"duration"`  // trace duration in seconds
	Container string `json:"container"` // container name or ID
	Hostname  string `json:"hostname"`  // host name
}

// StartTraceResponse represents a response to start tracing
type StartTraceResponse struct {
	ID string `json:"id"` // trace task ID
}

// TraceStatusResponse represents a trace status response
type TraceStatusResponse struct {
	ID           string       `json:"id"`            // trace task ID
	AgentTaskID  string       `json:"agent_task_id"` // agent task ID
	Container    string       `json:"container"`     // container name or ID
	Hostname     string       `json:"hostname"`      // host name
	Status       string       `json:"status"`        // task status
	StartTime    string       `json:"start_time"`    // start time
	EndTime      string       `json:"end_time"`      // end time
	Results      TraceResults `json:"results"`       // trace results
	ErrorMessage string       `json:"error_message"` // error message if any
}

// TraceResults represents trace results
type TraceResults struct {
	URL string `json:"url"` // URL to view the results
}

// PatchStatusRequest represents a request to patch the status of a job.
// Currently only "stopped" is accepted.
type PatchStatusRequest struct {
	Status string `json:"status"`
}

// TraceListResponse represents a paginated list of traces.
type TraceListResponse struct {
	Items  []TraceStatusResponse `json:"items"`
	Total  int                   `json:"total"`
	Limit  int                   `json:"limit"`
	Offset int                   `json:"offset"`
}

// ProfilingListResponse represents a paginated list of profiling jobs.
type ProfilingListResponse struct {
	Items  []ProfilingStatusResponse `json:"items"`
	Total  int                       `json:"total"`
	Limit  int                       `json:"limit"`
	Offset int                       `json:"offset"`
}

// ProfilingCapabilitiesResponse describes the profiling capabilities
// supported by the server and their default configurations.
type ProfilingCapabilitiesResponse struct {
	ProfileTypes                    []string          `json:"profile_types"`                       // supported profiling types, e.g. ["cpu", "memory"]
	CPUSupportedLanguages           []string          `json:"cpu_supported_languages"`             // languages supported by CPU profiling
	MemorySupportedLanguages        []string          `json:"memory_supported_languages"`          // languages supported by memory profiling
	MemoryModes                     map[string]string `json:"memory_modes"`                        // supported memory modes (key: display name, value: internal mode)
	DefaultCPUInterval              int               `json:"default_cpu_interval"`                // default CPU profiling interval in seconds
	DefaultMemoryInterval           int               `json:"default_memory_interval"`             // default memory profiling interval in seconds
	DefaultCPUSingleTraceTimeout    int               `json:"default_cpu_single_trace_timeout"`    // default CPU single trace timeout in seconds
	DefaultMemorySingleTraceTimeout int               `json:"default_memory_single_trace_timeout"` // default memory single trace timeout in seconds
	ThirdPartyToolLimit             int               `json:"third_party_tool_limit"`              // third-party tool limit
}

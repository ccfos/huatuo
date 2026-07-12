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

// UserResponse represents a registered user/API key in the access-control view.
// The ID field is the credential presented in the Authorization header.
type UserResponse struct {
	ID          string   `json:"id"`          // unique credential / API key
	Name        string   `json:"name"`        // human-readable display name
	IsAdmin     bool     `json:"is_admin"`    // whether the user has administrator privileges
	Permissions []string `json:"permissions"` // URL path patterns this user may access
}

// CreateUserRequest creates a new user/API key. When GenerateKey is true the
// server generates a random credential and returns it once in the response;
// the caller is then expected to treat it as a secret.
type CreateUserRequest struct {
	Name        string   `json:"name"`         // human-readable display name
	IsAdmin     bool     `json:"is_admin"`     // whether the user has administrator privileges
	Permissions []string `json:"permissions"`  // URL path patterns this user may access
	GenerateKey bool     `json:"generate_key"` // when true, generate a random API key/ID
	ID          string   `json:"id,omitempty"` // explicit ID; used when GenerateKey is false
}

// CreateUserResponse is returned when a user/API key is created.
type CreateUserResponse struct {
	ID string `json:"id"` // the credential to present in the Authorization header
}

// RoleResponse describes a named permission template that can be assigned to a user.
type RoleResponse struct {
	Name        string   `json:"name"`        // role identifier, e.g. "admin"
	Description string   `json:"description"` // human-readable description
	Permissions []string `json:"permissions"` // URL path patterns granted by the role
	IsAdmin     bool     `json:"is_admin"`    // whether the role implies administrator privileges
}

// WhoAmIResponse describes the identity of the authenticated caller.
type WhoAmIResponse struct {
	ID          string   `json:"id"`          // credential / API key
	Name        string   `json:"name"`        // display name
	IsAdmin     bool     `json:"is_admin"`    // administrator privileges
	Permissions []string `json:"permissions"` // effective URL path patterns (empty for admins)
}

// SystemInfoResponse aggregates status information for the dashboard.
type SystemInfoResponse struct {
	Version string       `json:"version"` // server version
	Commit  string       `json:"commit"`  // git commit
	Modules []ModuleInfo `json:"modules"` // integrated modules surfaced in the console
	Limits  SystemLimits `json:"limits"`  // configured task scheduling limits
}

// ModuleInfo describes an integrated observability module.
type ModuleInfo struct {
	Name        string `json:"name"`         // module identifier, e.g. "profiling"
	DisplayName string `json:"display_name"` // human-readable name
	Description string `json:"description"`  // short description of the module
	Endpoint    string `json:"endpoint"`     // primary API endpoint backing the module
	Enabled     bool   `json:"enabled"`      // whether the module is available
}

// SystemLimits exposes the configured task scheduling limits.
type SystemLimits struct {
	MaxProfilingTasksPerHost int `json:"max_profiling_tasks_per_host"`
	MaxTracingTasksPerHost   int `json:"max_tracing_tasks_per_host"`
	MaxTotalProfilingTasks   int `json:"max_total_profiling_tasks"`
	MaxTotalTracingTasks     int `json:"max_total_tracing_tasks"`
}

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
	Type                  string            `json:"type"`                       // cpu, memory, or lock
	TargetExecPath        string            `json:"target_exec_path"`           // executable path for CPU profiling
	TargetProcessLanguage string            `json:"target_process_language"`    // programming language of the target process
	MemoryMode            string            `json:"memory_mode"`                // memory profiling mode
	Duration              int               `json:"duration"`                   // profiling duration in seconds
	Container             string            `json:"container"`                  // container name or ID
	Hostname              string            `json:"hostname"`                   // host name
	CPUIds                []int             `json:"cpu_ids,omitempty"`          // target logical CPU IDs
	Scope                 string            `json:"scope,omitempty"`            // pid, tgid, cgroup, or process-group
	PID                   uint64            `json:"pid,omitempty"`              // target PID/TGID
	CgroupID              uint64            `json:"cgroup_id,omitempty"`        // target cgroup ID
	CgroupPath            string            `json:"cgroup_path,omitempty"`      // target cgroup filesystem path
	ProcessGroupID        int               `json:"process_group_id,omitempty"` // target process group ID
	LockTypes             []string          `json:"lock_types,omitempty"`       // mutex, spinlock, rwlock
	LockMode              string            `json:"lock_mode,omitempty"`        // time or count
	LockMinWait           string            `json:"lock_min_wait,omitempty"`    // Go duration such as 1us
	Labels                map[string]string `json:"labels,omitempty"`           // Pyroscope/Parca-compatible series labels
}

// StartProfilingResponse represents a response to start profiling
type StartProfilingResponse struct {
	ID string `json:"id"` // profiling task ID
}

// ProfilingStatusResponse represents a profiling status response
type ProfilingStatusResponse struct {
	ID                    string            `json:"id"`                      // profiling task ID
	AgentTaskID           string            `json:"agent_task_id"`           // agent task ID
	Container             string            `json:"container"`               // container name or ID
	Hostname              string            `json:"hostname"`                // host name
	Status                string            `json:"status"`                  // task status
	StartTime             string            `json:"start_time"`              // start time
	EndTime               string            `json:"end_time"`                // end time
	TracerArgs            []string          `json:"tracer_args"`             // tracer arguments
	Duration              int               `json:"duration"`                // profiling duration
	Results               ProfilingResults  `json:"results"`                 // profiling results
	ErrorMessage          string            `json:"error_message"`           // error message if any
	Type                  string            `json:"type"`                    // cpu, memory, or lock
	TargetExecPath        string            `json:"target_exec_path"`        // executable path for CPU profiling
	TargetProcessLanguage string            `json:"target_process_language"` // programming language of the target process
	MemoryMode            string            `json:"memory_mode"`             // memory profiling mode
	CPUIds                []int             `json:"cpu_ids,omitempty"`       // target logical CPU IDs
	Scope                 string            `json:"scope,omitempty"`
	PID                   uint64            `json:"pid,omitempty"`
	CgroupID              uint64            `json:"cgroup_id,omitempty"`
	CgroupPath            string            `json:"cgroup_path,omitempty"`
	ProcessGroupID        int               `json:"process_group_id,omitempty"`
	LockTypes             []string          `json:"lock_types,omitempty"`
	LockMode              string            `json:"lock_mode,omitempty"`
	LockMinWait           string            `json:"lock_min_wait,omitempty"`
	Labels                map[string]string `json:"labels,omitempty"`
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
	ProfileTypes                    []string          `json:"profile_types"`                       // supported profiling types, e.g. ["cpu", "memory", "lock"]
	CPUSupportedLanguages           []string          `json:"cpu_supported_languages"`             // languages supported by CPU profiling
	MemorySupportedLanguages        []string          `json:"memory_supported_languages"`          // languages supported by memory profiling
	LockSupportedLanguages          []string          `json:"lock_supported_languages"`            // languages supported by lock profiling
	MemoryModes                     map[string]string `json:"memory_modes"`                        // supported memory modes (key: display name, value: internal mode)
	DefaultCPUInterval              int               `json:"default_cpu_interval"`                // default CPU profiling interval in seconds
	DefaultMemoryInterval           int               `json:"default_memory_interval"`             // default memory profiling interval in seconds
	DefaultCPUSingleTraceTimeout    int               `json:"default_cpu_single_trace_timeout"`    // default CPU single trace timeout in seconds
	DefaultMemorySingleTraceTimeout int               `json:"default_memory_single_trace_timeout"` // default memory single trace timeout in seconds
	MaxProfilerProcesses            int               `json:"max_profiler_processes"`              // maximum concurrent profiler subprocesses
	CollectionDimensions            []string          `json:"collection_dimensions"`               // cpu, pid, tgid, cgroup, process-group
	KernelLockTypes                 []string          `json:"kernel_lock_types"`                   // mutex, spinlock, rwlock
}

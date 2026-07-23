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
	"time"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
)

// CreateProfilingJobRequest represents a request to create a profiling job.
type CreateProfilingJobRequest struct {
	ProfilingType   string `json:"type"`              // cpu or memory
	BinaryMatchPath string `json:"binary_match_path"` // executable path used to match target processes
	Language        string `json:"language"`          // programming language of the target process
	MemoryMode      string `json:"memory_mode"`       // memory profiling mode
	Duration        int    `json:"duration"`          // profiling duration in seconds
	ContainerID     string `json:"container_id"`      // container ID
	Hostname        string `json:"hostname"`          // host name
}

// CreateProfilingJobResponse represents a response to create a profiling job.
type CreateProfilingJobResponse struct {
	ID string `json:"id"` // profiling job ID
}

// ProfilingJobResponse represents a profiling job response.
type ProfilingJobResponse struct {
	ID              string           `json:"id"`                // profiling job ID
	AgentTaskID     string           `json:"agent_task_id"`     // agent task ID
	ContainerID     string           `json:"container_id"`      // container ID
	Hostname        string           `json:"hostname"`          // host name
	Type            string           `json:"type"`              // cpu or memory
	MemoryMode      string           `json:"memory_mode"`       // memory profiling mode
	Language        string           `json:"language"`          // programming language of the target process
	BinaryMatchPath string           `json:"binary_match_path"` // executable path used to match target processes
	Status          string           `json:"status"`            // job status
	StartTime       string           `json:"start_time"`        // start time
	EndTime         string           `json:"end_time"`          // end time
	TracerArgs      []string         `json:"tracer_args"`       // tracer arguments
	Duration        int              `json:"duration"`          // profiling duration
	Results         ProfilingResults `json:"results"`           // profiling results
	ErrorMessage    string           `json:"error_message"`     // error message if any
}

// ProfilingResults represents profiling results
type ProfilingResults struct {
	URL string `json:"url"` // URL to view the results
}

// RawProfile is the stable API representation of one stored profile.
type RawProfile struct {
	Hostname               string               `json:"hostname"`
	Region                 string               `json:"region"`
	UploadedTime           time.Time            `json:"uploaded_time"`
	Time                   string               `json:"time"`
	ContainerID            string               `json:"container_id,omitempty"`
	ContainerHostname      string               `json:"container_hostname,omitempty"`
	ContainerHostNamespace string               `json:"container_host_namespace,omitempty"`
	ContainerType          string               `json:"container_type,omitempty"`
	ContainerQOS           string               `json:"container_qos,omitempty"`
	TracerName             string               `json:"tracer_name,omitempty"`
	TracerID               string               `json:"tracer_id,omitempty"`
	TracerTime             string               `json:"tracer_time"`
	TracerRunType          string               `json:"tracer_type,omitempty"`
	TracerData             RawProfileTracerData `json:"tracer_data,omitempty"`
}

// RawProfileTracerData contains profiler output fields exposed by the API.
type RawProfileTracerData struct {
	Flamedata RawProfileFlameData `json:"flamedata,omitempty"`
}

// RawProfileFlameData contains the profile payload and its type.
type RawProfileFlameData struct {
	ProfileType string             `json:"profile_type,omitempty"`
	Profile     *profilev1.Profile `json:"profile,omitempty"`
}

// RawDataResponse represents a page of raw profiling data.
type RawDataResponse struct {
	Data    []RawProfile `json:"data"`
	Limit   int          `json:"limit"`
	Offset  int          `json:"offset"`
	HasMore bool         `json:"has_more"`
}

// JobFilter represents a job filter
type JobFilter struct {
	ContainerID string `json:"container_id"` // container ID
	Hostname    string `json:"hostname"`     // host name
	Status      string `json:"status"`       // job status
	Type        string `json:"type"`         // job type
}

// CreateTraceJobRequest represents a request to create a trace job.
type CreateTraceJobRequest struct {
	Type        string `json:"type"`         // trace type
	Duration    int    `json:"duration"`     // trace duration in seconds
	ContainerID string `json:"container_id"` // container ID
	Hostname    string `json:"hostname"`     // host name
}

// CreateTraceJobResponse represents a response to create a trace job.
type CreateTraceJobResponse struct {
	ID string `json:"id"` // trace job ID
}

// TraceJobResponse represents a trace job response.
type TraceJobResponse struct {
	ID           string       `json:"id"`            // trace job ID
	AgentTaskID  string       `json:"agent_task_id"` // agent task ID
	ContainerID  string       `json:"container_id"`  // container ID
	Hostname     string       `json:"hostname"`      // host name
	Type         string       `json:"type"`          // requested tracer type
	Status       string       `json:"status"`        // job status
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

// TraceJobListResponse represents a paginated list of trace jobs.
type TraceJobListResponse struct {
	Items  []TraceJobResponse `json:"items"`
	Total  int                `json:"total"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
}

// ProfilingJobListResponse represents a paginated list of profiling jobs.
type ProfilingJobListResponse struct {
	Items  []ProfilingJobResponse `json:"items"`
	Total  int                    `json:"total"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
}

// ProfilingCapabilitiesResponse describes the profiling capabilities
// supported by the server and their default configurations.
type ProfilingCapabilitiesResponse struct {
	Types               []string          `json:"types"`                // supported profiling types, e.g. ["cpu", "memory"]
	CPULanguages        []string          `json:"cpu_languages"`        // languages supported by CPU profiling
	MemoryLanguages     []string          `json:"memory_languages"`     // languages supported by memory profiling
	MemoryModes         map[string]string `json:"memory_modes"`         // supported memory modes (key: display name, value: internal mode)
	AggregationInterval int               `json:"aggregation_interval"` // default aggregation interval in seconds
	ExecutionTimeout    int               `json:"execution_timeout"`    // default profiler execution timeout in seconds
	MaxProfilerProcs    int               `json:"max_profiler_procs"`   // maximum concurrent profiler subprocesses
}

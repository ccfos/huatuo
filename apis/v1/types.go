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
	"time"
)

// CreateProfilingJobRequest represents a request to create a profiling job.
type CreateProfilingJobRequest struct {
	ProfilingType   string `json:"type"`              // cpu or memory
	BinaryMatchPath string `json:"binary_match_path"` // executable path used to match target processes
	Language        string `json:"language"`          // programming language of the target process
	MemoryMode      string `json:"memory_mode"`       // memory profiling mode
	DurationSeconds int    `json:"duration_seconds"`  // profiling duration in seconds
	ContainerID     string `json:"container_id"`      // container ID
	Hostname        string `json:"hostname"`          // host name
}

// CreateProfilingJobResponse represents a response to create a profiling job.
type CreateProfilingJobResponse struct {
	ID string `json:"id"` // profiling job ID
}

// ProfilingJob describes a profiling job exposed by the API.
type ProfilingJob struct {
	ID              string     `json:"id"`                          // profiling job ID
	ContainerID     string     `json:"container_id,omitempty"`      // container ID
	Hostname        string     `json:"hostname"`                    // host name
	Type            string     `json:"type"`                        // cpu or memory
	MemoryMode      string     `json:"memory_mode,omitempty"`       // memory profiling mode
	Language        string     `json:"language"`                    // programming language of the target process
	BinaryMatchPath string     `json:"binary_match_path,omitempty"` // executable path used to match target processes
	Status          string     `json:"status"`                      // job status
	DurationSeconds int        `json:"duration_seconds"`            // profiling duration in seconds
	CreatedAt       time.Time  `json:"created_at"`                  // job creation time
	FinishedAt      *time.Time `json:"finished_at"`                 // terminal status time
	ResultURL       *string    `json:"result_url"`                  // URL to view the results
	StatusReason    *string    `json:"status_reason"`               // reason for the current terminal status
}

// RawProfile describes one profiling window without exposing its storage layout.
type RawProfile struct {
	Hostname          string          `json:"hostname"`
	Region            string          `json:"region"`
	UploadedAt        time.Time       `json:"uploaded_at"`
	CapturedAt        time.Time       `json:"captured_at"`
	ContainerID       string          `json:"container_id,omitempty"`
	ContainerHostname string          `json:"container_hostname,omitempty"`
	ContainerType     string          `json:"container_type,omitempty"`
	ContainerQoS      string          `json:"container_qos,omitempty"`
	ProfileType       string          `json:"profile_type"`
	Profile           json.RawMessage `json:"profile"`
}

// RawProfilePage contains one page of profiling windows.
type RawProfilePage struct {
	Items   []RawProfile `json:"items"`
	Limit   int          `json:"limit"`
	Offset  int          `json:"offset"`
	HasMore bool         `json:"has_more"`
}

// CreateTraceJobRequest represents a request to create a trace job.
type CreateTraceJobRequest struct {
	Type            string `json:"type"`             // trace type
	DurationSeconds int    `json:"duration_seconds"` // trace duration in seconds
	ContainerID     string `json:"container_id"`     // container ID
	Hostname        string `json:"hostname"`         // host name
}

// CreateTraceJobResponse represents a response to create a trace job.
type CreateTraceJobResponse struct {
	ID string `json:"id"` // trace job ID
}

// TraceJob describes a trace job exposed by the API.
type TraceJob struct {
	ID              string     `json:"id"`                     // trace job ID
	ContainerID     string     `json:"container_id,omitempty"` // container ID
	Hostname        string     `json:"hostname"`               // host name
	Type            string     `json:"type"`                   // requested tracer type
	Status          string     `json:"status"`                 // job status
	DurationSeconds int        `json:"duration_seconds"`       // requested trace duration
	CreatedAt       time.Time  `json:"created_at"`             // job creation time
	FinishedAt      *time.Time `json:"finished_at"`            // terminal status time
	ResultURL       *string    `json:"result_url"`             // URL to view the result
	StatusReason    *string    `json:"status_reason"`          // reason for the terminal status
}

// PatchStatusRequest represents a request to patch the status of a job.
// Currently only "stopped" is accepted.
type PatchStatusRequest struct {
	Status string `json:"status"`
}

// TraceJobListResponse represents a paginated list of trace jobs.
type TraceJobListResponse struct {
	Items  []TraceJob `json:"items"`
	Total  int        `json:"total"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
}

// ProfilingJobListResponse represents a paginated list of profiling jobs.
type ProfilingJobListResponse struct {
	Items  []ProfilingJob `json:"items"`
	Total  int            `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

// ProfilingCapabilities describes supported profiling options and runtime limits.
type ProfilingCapabilities struct {
	Types                      []string            `json:"types"`                        // supported profiling types
	CPULanguages               []string            `json:"cpu_languages"`                // languages supported by CPU profiling
	MemoryLanguages            []string            `json:"memory_languages"`             // languages supported by memory profiling
	MemoryModes                map[string][]string `json:"memory_modes"`                 // supported modes by language
	AggregationIntervalSeconds int                 `json:"aggregation_interval_seconds"` // server aggregation interval
	MaxConcurrentProfilers     int                 `json:"max_concurrent_profilers"`     // concurrent profiler limit
}

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
	"time"
)

type JobStatus string

const (
	AgentStatusCompleted = "completed"
	AgentStatusFailed    = "failed"
	AgentStatusPending   = "pending"
	AgentStatusRunning   = "running"
	AgentStatusNotExist  = "not_exist"
)

const (
	JobStatusPending   JobStatus = "pending"
	JobStatusRunning   JobStatus = "running"
	JobStatusCompleted JobStatus = "completed"
	JobStatusFailed    JobStatus = "failed"
	JobStatusStopped   JobStatus = "stopped"
	JobStatusTimeout   JobStatus = "timeout"
)

// Result represents the result of a job
type Result struct {
	URL   string `json:"url"`
	Error string `json:"error"`
}

// NewAgentTaskReq represents the structure of the request body for creating a new job.
type NewAgentTaskReq struct {
	TracerName        string   `json:"tracer_name" binding:"required"`                  // Name of the tracer, required field
	TraceTimeout      int      `json:"trace_timeout" binding:"required,number,lt=3600"` // Timeout in seconds, must be less than 3600s(1 hours)
	Interval          int      `json:"interval" binding:"omitempty,number,lt=3600"`     // Interval in seconds, must be less than 3600s(1 hours)
	Duration          int      `json:"duration" binding:"omitempty,number,lt=86400"`    // Duration in seconds, must be less than 86400s(24 hours)
	DataType          string   `json:"data_type" binding:"required"`                    // Type of data to be handled, required field
	ContainerID       string   `json:"container_id" binding:"omitempty"`                // ID of the container, optional field
	ContainerHostname string   `json:"container_hostname" binding:"omitempty"`          // Hostname of the container, optional field
	TracerArgs        []string `json:"trace_args" binding:"omitempty"`                  // Additional arguments for the tracer, optional field
}

// CreateJobRequest holds parameters for creating a new job
type CreateJobRequest struct {
	UserID    string
	Container string
	Host      string
	JobType   string
	Args      *NewAgentTaskReq
}

// Job represents a job
type Job struct {
	Type        string          `json:"type"`
	JobID       string          `json:"job_id"`
	UserName    string          `json:"user_name"`
	UserID      string          `json:"user_id"`
	Container   string          `json:"container"`
	Host        string          `json:"host"`
	AgentTaskID string          `json:"agent_job_id"`
	Status      JobStatus       `json:"status"`
	Error       string          `json:"error,omitempty"`
	Duration    int             `json:"duration"`
	Timeout     int             `json:"timeout"`
	StartTime   time.Time       `json:"start_time"`
	EndTime     time.Time       `json:"end_time"`
	Args        NewAgentTaskReq `json:"args"`
	Results     Result          `json:"results,omitempty"`

	LastUpdate time.Time `json:"-"`
	stopChan   chan struct{}

	PrivateData map[string]any `json:"-"`
}

// JobQuery defines filters for searching jobs
type JobQuery struct {
	JobID     string
	UserID    string
	IsAdmin   bool
	Container string
	Host      string
	Status    string
	Type      string
}

// JobCleanupQuery defines parameters for cleaning up old jobs
type JobCleanupQuery struct {
	BeforeTime time.Time
}

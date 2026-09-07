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

	"huatuo-bamai/pkg/observation"
	"huatuo-bamai/pkg/profiling"
	tracingdomain "huatuo-bamai/pkg/tracing"
)

// Kind identifies the service that owns a persistent Job.
type Kind string

const (
	KindProfiling Kind = "profiling"
	KindTracing   Kind = "tracing"
)

// Status identifies a user-facing persistent Job state.
type Status string

const (
	StatusPending  Status = "pending"
	StatusRunning  Status = "running"
	StatusStopping Status = "stopping"
	StatusTerminal Status = "terminal"
)

// Outcome identifies the result of a terminal Job.
type Outcome string

const (
	OutcomeCompleted Outcome = "completed"
	OutcomeFailed    Outcome = "failed"
	OutcomeStopped   Outcome = "stopped"
	OutcomeUnknown   Outcome = "unknown"
)

// FailureReason classifies a failed Job independently of Node errors.
type FailureReason string

const (
	FailureReasonExecutionStartFailed      FailureReason = "execution_start_failed"
	FailureReasonExecutionCapacityExceeded FailureReason = "execution_capacity_exceeded"
	FailureReasonStartTimeout              FailureReason = "start_timeout"
	FailureReasonExecutionFailed           FailureReason = "execution_failed"
	FailureReasonExecutionTimedOut         FailureReason = "execution_timed_out"
	FailureReasonStopTimeout               FailureReason = "stop_timeout"
	FailureReasonNodeUnavailable           FailureReason = "node_unavailable"
	FailureReasonOperationLost             FailureReason = "operation_lost"
	FailureReasonInvalidNodeRequest        FailureReason = "invalid_node_request"
	FailureReasonProtocolError             FailureReason = "protocol_error"
)

// StopReason records why Apiserver requested asynchronous Node termination.
type StopReason string

const (
	StopReasonUser             StopReason = "user_requested"
	StopReasonStartTimeout     StopReason = "start_timeout"
	StopReasonExecutionTimeout StopReason = "execution_timeout"
)

// TerminalResult is the stable result of a terminal Job.
type TerminalResult struct {
	Outcome Outcome       `json:"outcome"`
	Reason  FailureReason `json:"reason,omitempty"`
	Message string        `json:"message,omitempty"`
}

// Spec is a small discriminated union of service-owned Job parameters.
type Spec struct {
	Profiling *profiling.Spec     `json:"profiling,omitempty"`
	Tracing   *tracingdomain.Spec `json:"tracing,omitempty"`
}

// Job is the persistent Apiserver lifecycle for one Node request ID.
type Job struct {
	ID          string
	Kind        Kind
	UserID      string
	Hostname    string
	Duration    time.Duration
	Scope       observation.Scope
	ContainerID string
	Spec        Spec

	Status    Status
	Terminal  *TerminalResult
	CreatedAt time.Time
	UpdatedAt time.Time
	StartedAt time.Time
	EndedAt   time.Time

	PendingDeadline         time.Time
	ExecutionDeadline       time.Time
	NodeUnavailableDeadline time.Time
	StopDeadline            time.Time
	StopReason              StopReason

	revision int64
}

// CreateRequest contains one independently created user request.
type CreateRequest struct {
	UserID      string
	Hostname    string
	Duration    time.Duration
	Scope       observation.Scope
	ContainerID string
	Spec        Spec
}

// Query filters persistent Jobs through derived storage indexes.
type Query struct {
	ID          string
	UserID      string
	IsAdmin     bool
	ContainerID string
	Hostname    string
	Statuses    []Status
	Kinds       []Kind
	Subtypes    []string
	Sort        string
	Limit       int
	Offset      int
}

// Page contains one Job page and whether another page is available.
type Page struct {
	Items   []*Job
	HasMore bool
}

func (s Spec) kind() Kind {
	switch {
	case s.Profiling != nil && s.Tracing == nil:
		return KindProfiling
	case s.Tracing != nil && s.Profiling == nil:
		return KindTracing
	default:
		return ""
	}
}

func (s Spec) subtype(kind Kind) string {
	switch kind {
	case KindProfiling:
		if s.Profiling != nil {
			return string(s.Profiling.Type)
		}
	case KindTracing:
		if s.Tracing != nil {
			return string(s.Tracing.Type)
		}
	}
	return ""
}

func cloneJob(source *Job) *Job {
	if source == nil {
		return nil
	}
	cloned := *source
	if source.Spec.Profiling != nil {
		profilingSpec := *source.Spec.Profiling
		cloned.Spec.Profiling = &profilingSpec
	}
	if source.Spec.Tracing != nil {
		tracingSpec := *source.Spec.Tracing
		cloned.Spec.Tracing = &tracingSpec
	}
	if source.Terminal != nil {
		terminal := *source.Terminal
		cloned.Terminal = &terminal
	}
	return &cloned
}

func isTerminal(status Status) bool {
	return status == StatusTerminal
}

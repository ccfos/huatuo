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
	"errors"
	"fmt"
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

func (r *CreateRequest) validate() error {
	if r == nil {
		return errors.New("request is required")
	}
	if r.UserID == "" {
		return errors.New("job user ID is required")
	}
	if r.Hostname == "" {
		return errors.New("job hostname is required")
	}
	if r.Duration <= 0 || r.Duration%time.Second != 0 {
		return errors.New("job duration must be a whole positive number of seconds")
	}
	if err := observation.ValidateScope(r.Scope, r.ContainerID); err != nil {
		return fmt.Errorf("validate job scope: %w", err)
	}
	if err := r.Spec.validate(r.Spec.kind(), r.Scope); err != nil {
		return err
	}
	return nil
}

func (j *Job) validateStored() error {
	if j == nil {
		return errors.New("job is nil")
	}
	if j.ID == "" {
		return errors.New("job ID is required")
	}
	if j.revision <= 0 {
		return errors.New("job storage revision is required")
	}
	if j.UserID == "" {
		return errors.New("job user ID is required")
	}
	if j.Hostname == "" {
		return errors.New("job hostname is required")
	}
	if j.Duration <= 0 || j.Duration%time.Second != 0 {
		return errors.New("job duration must be a whole positive number of seconds")
	}
	if err := observation.ValidateScope(j.Scope, j.ContainerID); err != nil {
		return fmt.Errorf("validate job scope: %w", err)
	}
	if err := j.Spec.validate(j.Kind, j.Scope); err != nil {
		return err
	}
	if j.CreatedAt.IsZero() || j.UpdatedAt.IsZero() {
		return errors.New("job created and updated timestamps are required")
	}
	return j.validateState()
}

func (j *Job) validateState() error {
	if !isValidStatus(j.Status) {
		return fmt.Errorf("unsupported job status %q", j.Status)
	}
	if (j.Status == StatusTerminal) != (j.Terminal != nil) {
		return errors.New("job terminal result must be present exactly when status is terminal")
	}
	if j.Terminal != nil {
		if !isValidOutcome(j.Terminal.Outcome) {
			return fmt.Errorf("unsupported job terminal outcome %q", j.Terminal.Outcome)
		}
		if j.Terminal.Outcome == OutcomeFailed && !isValidFailureReason(j.Terminal.Reason) {
			return fmt.Errorf("unsupported job failure reason %q", j.Terminal.Reason)
		}
	}
	if isTerminal(j.Status) != !j.EndedAt.IsZero() {
		return errors.New("job ended timestamp must be present exactly when status is terminal")
	}
	if !isValidStopReason(j.StopReason) {
		return fmt.Errorf("unsupported job stop reason %q", j.StopReason)
	}
	if j.Status == StatusRunning && (j.StartedAt.IsZero() || j.ExecutionDeadline.IsZero()) {
		return errors.New("running job requires started and execution deadline timestamps")
	}
	if j.Status == StatusStopping &&
		(j.StopReason == "" || j.StopDeadline.IsZero()) {
		return errors.New("stopping job requires a persisted stop intent and deadline")
	}
	return nil
}

func (s Spec) validate(kind Kind, scope observation.Scope) error {
	switch kind {
	case KindProfiling:
		if s.Profiling == nil || s.Tracing != nil {
			return errors.New("profiling job must contain only a profiling spec")
		}
		if err := s.Profiling.Validate(); err != nil {
			return fmt.Errorf("validate profiling job spec: %w", err)
		}
		if !profiling.SupportsScope(s.Profiling.Language, s.Profiling.Type, scope) {
			return fmt.Errorf("profiling job does not support scope %q", scope)
		}
	case KindTracing:
		if s.Tracing == nil || s.Profiling != nil {
			return errors.New("tracing job must contain only a tracing spec")
		}
		if err := s.Tracing.Validate(); err != nil {
			return fmt.Errorf("validate tracing job spec: %w", err)
		}
		if !tracingdomain.SupportsScope(s.Tracing.Type, scope) {
			return fmt.Errorf("tracing job does not support scope %q", scope)
		}
	default:
		return fmt.Errorf("unsupported job kind %q", kind)
	}
	return nil
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

func isValidStatus(status Status) bool {
	switch status {
	case StatusPending,
		StatusRunning,
		StatusStopping,
		StatusTerminal:
		return true
	default:
		return false
	}
}

func isTerminal(status Status) bool {
	return status == StatusTerminal
}

func isValidOutcome(outcome Outcome) bool {
	return outcome == OutcomeCompleted || outcome == OutcomeFailed ||
		outcome == OutcomeStopped || outcome == OutcomeUnknown
}

func isValidFailureReason(reason FailureReason) bool {
	switch reason {
	case FailureReasonExecutionStartFailed,
		FailureReasonExecutionCapacityExceeded,
		FailureReasonStartTimeout,
		FailureReasonExecutionFailed,
		FailureReasonExecutionTimedOut,
		FailureReasonStopTimeout,
		FailureReasonNodeUnavailable,
		FailureReasonOperationLost,
		FailureReasonProtocolError:
		return true
	default:
		return false
	}
}

func isValidStopReason(reason StopReason) bool {
	switch reason {
	case "", StopReasonUser, StopReasonStartTimeout, StopReasonExecutionTimeout:
		return true
	default:
		return false
	}
}

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

// Package operation owns the Node Agent's in-memory execution lifecycle.
package operation

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrStopped indicates that an executor exited because Stop terminated it.
	ErrStopped = errors.New("operation: stopped")

	// ErrInvalidRequest indicates that required operation input is missing.
	ErrInvalidRequest = errors.New("operation: invalid request")
	// ErrNotFound indicates that an operation is absent or no longer retained.
	ErrNotFound = errors.New("operation: not found")
	// ErrRequestIDConflict indicates that a request ID belongs to another kind.
	ErrRequestIDConflict = errors.New("operation: request id conflict")
	// ErrLimitExceeded indicates that all operation capacity is occupied.
	ErrLimitExceeded = errors.New("operation: limit exceeded")
	// ErrShuttingDown indicates that the manager no longer accepts operations.
	ErrShuttingDown = errors.New("operation: shutting down")
)

// Kind identifies the service that owns an operation.
type Kind string

const (
	KindProfiling Kind = "profiling"
	KindTracing   Kind = "tracing"
)

// Status is the public state of a Node operation.
type Status string

const (
	StatusPending  Status = "pending"
	StatusRunning  Status = "running"
	StatusStopping Status = "stopping"
	StatusTerminal Status = "terminal"
)

// Outcome identifies the result of a terminal operation.
type Outcome string

const (
	OutcomeCompleted Outcome = "completed"
	OutcomeFailed    Outcome = "failed"
	OutcomeStopped   Outcome = "stopped"
)

// FailureReason classifies a lifecycle failure independently of HTTP.
type FailureReason string

const (
	FailureReasonExecutionStartFailed FailureReason = "execution_start_failed"
	FailureReasonLaunchTimeout        FailureReason = "launch_timeout"
	FailureReasonExecutionFailed      FailureReason = "execution_failed"
	FailureReasonExecutionStopFailed  FailureReason = "execution_stop_failed"
	FailureReasonFinalizationFailed   FailureReason = "finalization_failed"
	FailureReasonFinalizationTimeout  FailureReason = "finalization_timeout"
)

// TerminalResult is the stable result of a terminal operation.
type TerminalResult struct {
	Outcome Outcome
	Reason  FailureReason
	Message string
}

// Operation is an immutable snapshot returned by Manager.
type Operation struct {
	RequestID  string
	Kind       Kind
	Status     Status
	Terminal   *TerminalResult
	CreatedAt  time.Time
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// FinalizeMode controls whether an executor publishes or discards its result.
type FinalizeMode uint8

const (
	FinalizePublish FinalizeMode = iota + 1
	FinalizeDiscard
)

// Executor owns the resources for one operation execution.
type Executor interface {
	Start(ctx context.Context) error
	Wait() error
	Stop(ctx context.Context) error
	Finalize(ctx context.Context, mode FinalizeMode) error
}

// LifecyclePolicy defines independent limits for Node-local lifecycle phases.
type LifecyclePolicy struct {
	LaunchTimeout           time.Duration
	StopGracePeriod         time.Duration
	FinalizationTimeout     time.Duration
	TerminalRetentionPeriod time.Duration
}

// Config configures a process-wide operation Manager.
type Config struct {
	MaxConcurrent int
	Lifecycle     LifecyclePolicy
}

// StartRequest contains the common input needed to create an operation.
type StartRequest struct {
	RequestID string
	Kind      Kind
	Executor  Executor
}

func isTerminal(status Status) bool {
	return status == StatusTerminal
}

func isValidKind(kind Kind) bool {
	return kind == KindProfiling || kind == KindTracing
}

func cloneOperation(operation *Operation) *Operation {
	clone := *operation
	if operation.Terminal != nil {
		terminal := *operation.Terminal
		clone.Terminal = &terminal
	}
	if operation.StartedAt != nil {
		startedAt := *operation.StartedAt
		clone.StartedAt = &startedAt
	}
	if operation.FinishedAt != nil {
		finishedAt := *operation.FinishedAt
		clone.FinishedAt = &finishedAt
	}
	return &clone
}

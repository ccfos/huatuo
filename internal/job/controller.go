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
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	apiv1 "huatuo-bamai/apis/v1"
	nodeapi "huatuo-bamai/apis/v1/node"
	"huatuo-bamai/internal/nodeclient"
)

func (m *Manager) superviseOnce(ctx context.Context, runtime *managedJob) (bool, error) {
	runtime.mu.Lock()
	if isTerminal(runtime.job.Status) {
		runtime.mu.Unlock()
		return true, nil
	}
	shouldStart := runtime.job.Status == StatusPending &&
		runtime.job.StartAttemptedAt.IsZero()
	runtime.mu.Unlock()

	if shouldStart {
		operation, err := m.start(ctx, runtime)
		if errors.Is(err, ErrShuttingDown) {
			return false, nil
		}
		if err != nil {
			if errors.Is(err, ErrPersistence) || errors.Is(err, ErrConflict) {
				return false, err
			}
			return m.handleNodeError(ctx, runtime, err, true)
		}
		return m.reconcileAndStop(ctx, runtime, operation)
	}

	snapshot := runtimeSnapshot(runtime)
	operation, err := m.getOperation(ctx, snapshot)
	if err != nil {
		return m.handleNodeError(ctx, runtime, err, false)
	}
	return m.reconcileAndStop(ctx, runtime, operation)
}

func (m *Manager) start(
	ctx context.Context,
	runtime *managedJob,
) (*nodeapi.Operation, error) {
	select {
	case <-ctx.Done():
		return nil, ErrShuttingDown
	default:
	}

	runtime.mu.Lock()
	current := runtime.job
	if current.Status != StatusPending || !current.StartAttemptedAt.IsZero() {
		runtime.mu.Unlock()
		return nil, fmt.Errorf("%w: Job %q cannot be dispatched", ErrConflict, current.ID)
	}
	now := m.now()
	updated := cloneJob(current)
	updated.StartAttemptedAt = now
	updated.PendingDeadline = now.Add(m.config.PendingTimeout)
	updated.UpdatedAt = now
	if err := m.store.Save(ctx, updated, StatusPending); err != nil {
		m.persistenceFailures.Add(1)
		runtime.mu.Unlock()
		return nil, fmt.Errorf(
			"%w: persist dispatch marker for Job %q: %w",
			ErrPersistence,
			current.ID,
			err,
		)
	}
	runtime.job = updated
	runtime.mu.Unlock()
	return m.startOperation(ctx, updated)
}

func (m *Manager) reconcileAndStop(
	ctx context.Context,
	runtime *managedJob,
	operation *nodeapi.Operation,
) (bool, error) {
	terminal, shouldStop, err := m.reconcileOperation(ctx, runtime, operation)
	if err != nil || terminal || !shouldStop {
		return terminal, err
	}

	snapshot := runtimeSnapshot(runtime)
	stoppedOperation, err := m.stopOperation(ctx, snapshot)
	if err != nil {
		return m.handleNodeError(ctx, runtime, err, false)
	}
	terminal, _, err = m.reconcileOperation(ctx, runtime, stoppedOperation)
	return terminal, err
}

func (m *Manager) reconcileOperation(
	ctx context.Context,
	runtime *managedJob,
	operation *nodeapi.Operation,
) (terminal, shouldStop bool, err error) {
	if operation == nil {
		return false, false, fmt.Errorf("%w: Node returned a nil Operation", nodeclient.ErrProtocol)
	}

	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	current := runtime.job
	if isTerminal(current.Status) {
		return true, false, nil
	}
	runtime.operationObserved = true

	now := m.now()
	updated := cloneJob(current)
	changed := clearNodeUnavailable(updated)
	if changed {
		updated.UpdatedAt = now
	}
	switch operation.Status {
	case nodeapi.OperationStatusPending:
		if current.Status == StatusRunning {
			setTerminal(updated, StatusFailed, &TerminalFailure{
				Reason:  FailureReasonProtocolError,
				Message: "Node Operation regressed from running to pending",
			}, now)
			changed = true
		} else if current.Status == StatusPending && !now.Before(current.PendingDeadline) {
			setStopping(updated, StopReasonStartTimeout, now, m.config.CompletionGracePeriod)
			changed = true
		}
	case nodeapi.OperationStatusRunning:
		if current.Status == StatusPending {
			updated.Status = StatusRunning
			updated.StartedAt = now
			updated.ExecutionDeadline = now.Add(current.Duration).Add(
				m.config.CompletionGracePeriod,
			)
			updated.UpdatedAt = now
			changed = true
		} else if current.Status == StatusRunning && !now.Before(current.ExecutionDeadline) {
			setStopping(updated, StopReasonExecutionTimeout, now, m.config.CompletionGracePeriod)
			changed = true
		}
	case nodeapi.OperationStatusStopping:
		if current.Status != StatusStopping {
			setTerminal(updated, StatusFailed, &TerminalFailure{
				Reason:  FailureReasonProtocolError,
				Message: "Node Operation stopped without a persisted Job stop intent",
			}, now)
			changed = true
		} else if !now.Before(current.StopDeadline) {
			setTerminal(updated, StatusFailed, &TerminalFailure{
				Reason:  FailureReasonStopTimeout,
				Message: "Node Operation did not stop before the Job stop deadline",
			}, now)
			changed = true
		}
	case nodeapi.OperationStatusCompleted:
		setTerminal(updated, StatusCompleted, nil, now)
		changed = true
	case nodeapi.OperationStatusFailed:
		failure := mapOperationFailure(operation.Failure)
		setTerminal(updated, StatusFailed, failure, now)
		changed = true
	case nodeapi.OperationStatusStopped:
		status, failure := stoppedJobOutcome(current.StopReason)
		setTerminal(updated, status, failure, now)
		changed = true
	default:
		setTerminal(updated, StatusFailed, &TerminalFailure{
			Reason:  FailureReasonProtocolError,
			Message: "Node returned an unsupported Operation status",
		}, now)
		changed = true
	}

	if updated.Status == StatusStopping &&
		(operation.Status == nodeapi.OperationStatusPending ||
			operation.Status == nodeapi.OperationStatusRunning) {
		if !now.Before(updated.StopDeadline) {
			setTerminal(updated, StatusFailed, &TerminalFailure{
				Reason:  FailureReasonStopTimeout,
				Message: "Node Operation did not stop before the Job stop deadline",
			}, now)
			changed = true
		} else {
			shouldStop = true
		}
	}

	if changed {
		if err := m.store.Save(ctx, updated, current.Status); err != nil {
			m.persistenceFailures.Add(1)
			return false, false, fmt.Errorf(
				"%w: reconcile Job %q: %w",
				ErrPersistence,
				current.ID,
				err,
			)
		}
		runtime.job = updated
	}
	return isTerminal(updated.Status), shouldStop, nil
}

func (m *Manager) handleNodeError(
	ctx context.Context,
	runtime *managedJob,
	err error,
	duringStart bool,
) (bool, error) {
	if err == nil {
		return false, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	current := runtime.job
	if isTerminal(current.Status) {
		return true, nil
	}
	now := m.now()
	updated := cloneJob(current)

	if isOperationNotFound(err) {
		if runtime.operationObserved {
			setTerminal(updated, StatusFailed, &TerminalFailure{
				Reason:  FailureReasonOperationLost,
				Message: "Node no longer has the previously observed Operation",
			}, now)
		} else {
			setTerminal(updated, StatusOutcomeUnknown, nil, now)
		}
	} else if failure := explicitNodeFailure(err, duringStart); failure != nil {
		setTerminal(updated, StatusFailed, failure, now)
	} else if isRecoverableNodeError(err) {
		if current.NodeUnavailableSince.IsZero() {
			updated.NodeUnavailableSince = now
			updated.NodeUnavailableDeadline = now.Add(m.config.NodeUnavailableGracePeriod)
			updated.UpdatedAt = now
		} else if !now.Before(current.NodeUnavailableDeadline) {
			setTerminal(updated, StatusFailed, &TerminalFailure{
				Reason:  FailureReasonNodeUnavailable,
				Message: "Node remained unavailable beyond the recovery window",
			}, now)
		} else {
			return false, nil
		}
	} else {
		setTerminal(updated, StatusFailed, &TerminalFailure{
			Reason:  FailureReasonProtocolError,
			Message: "Node response violated the Operation protocol",
		}, now)
	}

	if err := m.store.Save(ctx, updated, current.Status); err != nil {
		m.persistenceFailures.Add(1)
		return false, fmt.Errorf(
			"%w: persist Node error for Job %q: %w",
			ErrPersistence,
			current.ID,
			err,
		)
	}
	runtime.job = updated
	return isTerminal(updated.Status), nil
}

func (m *Manager) nextWake(runtime *managedJob) time.Duration {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	current := runtime.job
	if isTerminal(current.Status) {
		return 0
	}
	now := m.now()
	next := now.Add(m.config.StatusPollInterval)
	deadlines := []time.Time{current.NodeUnavailableDeadline}
	if current.NodeUnavailableSince.IsZero() {
		switch current.Status {
		case StatusPending:
			deadlines = append(deadlines, current.PendingDeadline)
		case StatusRunning:
			deadlines = append(deadlines, current.ExecutionDeadline)
		case StatusStopping:
			deadlines = append(deadlines, current.StopDeadline)
		}
	}
	for _, deadline := range deadlines {
		if !deadline.IsZero() && deadline.Before(next) {
			next = deadline
		}
	}
	return max(next.Sub(now), 0)
}

func (m *Manager) startOperation(
	ctx context.Context,
	job *Job,
) (*nodeapi.Operation, error) {
	switch job.Kind {
	case KindProfiling:
		return m.nodeClient.StartProfiling(ctx, job.Hostname, profilingStartRequest(job))
	case KindTracing:
		return m.nodeClient.StartTracing(ctx, job.Hostname, tracingStartRequest(job))
	default:
		return nil, fmt.Errorf("%w: unsupported Job kind %q", ErrUnsupportedKind, job.Kind)
	}
}

func (m *Manager) getOperation(
	ctx context.Context,
	job *Job,
) (*nodeapi.Operation, error) {
	switch job.Kind {
	case KindProfiling:
		return m.nodeClient.GetProfiling(ctx, job.Hostname, job.ID)
	case KindTracing:
		return m.nodeClient.GetTracing(ctx, job.Hostname, job.ID)
	default:
		return nil, fmt.Errorf("%w: unsupported Job kind %q", ErrUnsupportedKind, job.Kind)
	}
}

func (m *Manager) stopOperation(
	ctx context.Context,
	job *Job,
) (*nodeapi.Operation, error) {
	switch job.Kind {
	case KindProfiling:
		return m.nodeClient.StopProfiling(ctx, job.Hostname, job.ID)
	case KindTracing:
		return m.nodeClient.StopTracing(ctx, job.Hostname, job.ID)
	default:
		return nil, fmt.Errorf("%w: unsupported Job kind %q", ErrUnsupportedKind, job.Kind)
	}
}

func profilingStartRequest(job *Job) *nodeapi.StartProfilingRequest {
	spec := job.Spec.Profiling
	request := &nodeapi.StartProfilingRequest{
		RequestID:       job.ID,
		DurationSeconds: int64(job.Duration / time.Second),
		Scope:           apiv1.ObservationScope(job.Scope),
		Type:            nodeapi.ProfilingType(spec.Type),
		Language:        nodeapi.ProfilingLanguage(spec.Language),
		Mode:            nodeapi.ProfilingMode(spec.Mode),
	}
	if job.ContainerID != "" {
		request.ContainerID = &job.ContainerID
	}
	if spec.BinaryMatchPath != "" {
		request.BinaryMatchPath = &spec.BinaryMatchPath
	}
	return request
}

func tracingStartRequest(job *Job) *nodeapi.StartTracingRequest {
	request := &nodeapi.StartTracingRequest{
		RequestID:       job.ID,
		DurationSeconds: int64(job.Duration / time.Second),
		Scope:           apiv1.ObservationScope(job.Scope),
		Type:            nodeapi.TracingType(job.Spec.Tracing.Type),
	}
	if job.ContainerID != "" {
		request.ContainerID = &job.ContainerID
	}
	return request
}

func explicitNodeFailure(err error, duringStart bool) *TerminalFailure {
	var nodeErr *nodeclient.Error
	if !errors.As(err, &nodeErr) {
		if errors.Is(err, nodeclient.ErrProtocol) || errors.Is(err, nodeclient.ErrInvalidArgument) {
			return &TerminalFailure{
				Reason:  FailureReasonProtocolError,
				Message: "Node response violated the Operation protocol",
			}
		}
		return nil
	}
	if !duringStart {
		return nil
	}
	switch nodeErr.Code {
	case nodeapi.ErrorCodeOperationLimitExceeded:
		return &TerminalFailure{
			Reason:  FailureReasonExecutionCapacityExceeded,
			Message: nodeErr.Message,
		}
	case nodeapi.ErrorCodeExecutionEnvironmentUnsupported,
		nodeapi.ErrorCodeServiceNotImplemented,
		nodeapi.ErrorCodeExecutionStartFailed,
		nodeapi.ErrorCodeLaunchTimeout:
		return &TerminalFailure{
			Reason:  FailureReasonExecutionStartFailed,
			Message: nodeErr.Message,
		}
	default:
		return &TerminalFailure{
			Reason:  FailureReasonProtocolError,
			Message: nodeErr.Message,
		}
	}
}

func isRecoverableNodeError(err error) bool {
	var nodeErr *nodeclient.Error
	if errors.As(err, &nodeErr) {
		return nodeErr.StatusCode >= http.StatusInternalServerError
	}
	if errors.Is(err, nodeclient.ErrProtocol) || errors.Is(err, nodeclient.ErrInvalidArgument) {
		return false
	}
	return true
}

func isOperationNotFound(err error) bool {
	var nodeErr *nodeclient.Error
	return errors.As(err, &nodeErr) && nodeErr.Code == nodeapi.ErrorCodeOperationNotFound
}

func mapOperationFailure(failure *nodeapi.OperationFailure) *TerminalFailure {
	if failure == nil {
		return &TerminalFailure{
			Reason:  FailureReasonProtocolError,
			Message: "failed Node Operation did not include a failure",
		}
	}
	reason := FailureReasonProtocolError
	switch failure.Code {
	case nodeapi.ErrorCodeExecutionStartFailed, nodeapi.ErrorCodeLaunchTimeout:
		reason = FailureReasonExecutionStartFailed
	case nodeapi.ErrorCodeExecutionFailed,
		nodeapi.ErrorCodeExecutionStopFailed,
		nodeapi.ErrorCodeFinalizationFailed,
		nodeapi.ErrorCodeFinalizationTimeout:
		reason = FailureReasonExecutionFailed
	}
	return &TerminalFailure{Reason: reason, Message: failure.Message}
}

func stoppedJobOutcome(reason StopReason) (Status, *TerminalFailure) {
	switch reason {
	case StopReasonUser:
		return StatusStopped, nil
	case StopReasonStartTimeout:
		return StatusFailed, &TerminalFailure{
			Reason:  FailureReasonStartTimeout,
			Message: "Node Operation did not start before the Job pending deadline",
		}
	case StopReasonExecutionTimeout:
		return StatusFailed, &TerminalFailure{
			Reason:  FailureReasonExecutionTimedOut,
			Message: "Node Operation exceeded the Job execution deadline",
		}
	default:
		return StatusFailed, &TerminalFailure{
			Reason:  FailureReasonExecutionFailed,
			Message: "Node Operation stopped without a persisted Job stop reason",
		}
	}
}

func setStopping(job *Job, reason StopReason, now time.Time, gracePeriod time.Duration) {
	job.Status = StatusStopping
	job.StopReason = reason
	job.StopRequestedAt = now
	job.StopDeadline = now.Add(gracePeriod)
	job.UpdatedAt = now
}

func setTerminal(
	job *Job,
	status Status,
	failure *TerminalFailure,
	now time.Time,
) {
	job.Status = status
	job.Failure = failure
	job.UpdatedAt = now
	if job.EndedAt.IsZero() {
		job.EndedAt = now
	}
}

func clearNodeUnavailable(job *Job) bool {
	if job.NodeUnavailableSince.IsZero() && job.NodeUnavailableDeadline.IsZero() {
		return false
	}
	job.NodeUnavailableSince = time.Time{}
	job.NodeUnavailableDeadline = time.Time{}
	return true
}

func runtimeSnapshot(runtime *managedJob) *Job {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return cloneJob(runtime.job)
}

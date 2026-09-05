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
	"hash/fnv"
	"net/http"
	"time"

	nodeapi "huatuo-bamai/apis/v1/node"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/nodeclient"
)

func (m *Manager) runSupervisor(ctx context.Context, runtime *managedJob) {
	defer m.wg.Done()
	defer func() {
		m.mu.Lock()
		m.unregisterLocked(runtime)
		m.mu.Unlock()
	}()

	if runtime.recovered {
		if !waitForSupervisor(ctx, runtime.wake, recoveredPollJitter(
			runtime.id,
			m.config.StatusPollInterval,
		)) {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		terminal, err := m.superviseOnce(ctx, runtime)
		if errors.Is(err, ErrConflict) {
			terminal, err = m.reloadRuntime(ctx, runtime)
		}
		if err != nil && ctx.Err() == nil {
			log.WithError(err).WithField("job_id", runtime.id).
				Error("failed to supervise Job")
		}
		if terminal {
			return
		}
		if !waitForSupervisor(ctx, runtime.wake, m.nextSupervisorDelay(runtime, err)) {
			return
		}
	}
}

func (m *Manager) superviseOnce(ctx context.Context, runtime *managedJob) (bool, error) {
	runtime.mu.Lock()
	if isTerminal(runtime.job.Status) {
		runtime.mu.Unlock()
		return true, nil
	}
	shouldStart := runtime.job.Status == StatusPending &&
		runtime.job.PendingDeadline.IsZero()
	runtime.mu.Unlock()

	var (
		operation *nodeapi.Operation
		err       error
	)
	if shouldStart {
		operation, err = m.start(ctx, runtime)
		if err != nil {
			if errors.Is(err, ErrPersistence) || errors.Is(err, ErrConflict) {
				return false, err
			}
			return m.handleNodeError(ctx, runtime, err, true)
		}
	} else {
		snapshot := runtimeSnapshot(runtime)
		operation, err = m.nodeClient.GetOperation(ctx, snapshot.Hostname, snapshot.ID)
		if err != nil {
			return m.handleNodeError(ctx, runtime, err, false)
		}
	}

	terminal, reconcileErr := m.reconcileAndStop(ctx, runtime, operation)
	if errors.Is(reconcileErr, nodeclient.ErrProtocol) {
		return m.handleNodeError(ctx, runtime, reconcileErr, false)
	}
	return terminal, reconcileErr
}

func (m *Manager) reloadRuntime(ctx context.Context, runtime *managedJob) (bool, error) {
	runtime.mu.Lock()
	current := runtime.job
	runtime.mu.Unlock()

	stored, err := m.store.Get(ctx, runtime.id)
	if err != nil {
		return false, fmt.Errorf("%w: reload Job %q: %w", ErrPersistence, runtime.id, err)
	}
	if stored.ID != runtime.id || stored.Kind != runtime.kind || stored.Hostname != runtime.hostname {
		return false, fmt.Errorf(
			"%w: reload Job %q: immutable identity changed",
			ErrPersistence,
			runtime.id,
		)
	}

	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.job != current {
		return isTerminal(runtime.job.Status), nil
	}
	runtime.job = stored
	return isTerminal(stored.Status), nil
}

func (m *Manager) start(
	ctx context.Context,
	runtime *managedJob,
) (*nodeapi.Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	runtime.mu.Lock()
	current := runtime.job
	if current.Status != StatusPending || !current.PendingDeadline.IsZero() {
		runtime.mu.Unlock()
		return nil, fmt.Errorf("%w: Job %q cannot be dispatched", ErrConflict, current.ID)
	}
	now := m.now()
	updated := cloneJob(current)
	updated.PendingDeadline = now.Add(m.config.PendingTimeout)
	updated.UpdatedAt = now
	runtime.mu.Unlock()
	if err := m.persistRuntime(ctx, runtime, current, updated); err != nil {
		if errors.Is(err, ErrConflict) {
			return nil, fmt.Errorf("%w: persist dispatch marker for Job %q: %w", ErrConflict, current.ID, err)
		}
		return nil, fmt.Errorf(
			"%w: persist dispatch marker for Job %q: %w",
			ErrPersistence,
			current.ID,
			err,
		)
	}
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
	stoppedOperation, err := m.nodeClient.StopOperation(ctx, snapshot.Hostname, snapshot.ID)
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
	current := runtime.job
	if isTerminal(current.Status) {
		runtime.mu.Unlock()
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
			setTerminal(updated, &TerminalResult{
				Outcome: OutcomeFailed,
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
			setTerminal(updated, &TerminalResult{
				Outcome: OutcomeFailed,
				Reason:  FailureReasonProtocolError,
				Message: "Node Operation stopped without a persisted Job stop intent",
			}, now)
			changed = true
		} else if !now.Before(current.StopDeadline) {
			setTerminal(updated, &TerminalResult{
				Outcome: OutcomeFailed,
				Reason:  FailureReasonStopTimeout,
				Message: "Node Operation did not stop before the Job stop deadline",
			}, now)
			changed = true
		}
	case nodeapi.OperationStatusTerminal:
		if operation.Terminal == nil {
			setTerminal(updated, &TerminalResult{
				Outcome: OutcomeFailed,
				Reason:  FailureReasonProtocolError,
				Message: "terminal Node Operation did not include terminal details",
			}, now)
			changed = true
			break
		}
		switch operation.Terminal.Outcome {
		case nodeapi.OperationOutcomeCompleted:
			setTerminal(updated, &TerminalResult{Outcome: OutcomeCompleted}, now)
		case nodeapi.OperationOutcomeFailed:
			setTerminal(updated, mapOperationFailure(operation.Terminal), now)
		case nodeapi.OperationOutcomeStopped:
			setTerminal(updated, stoppedJobOutcome(current.StopReason), now)
		default:
			setTerminal(updated, &TerminalResult{
				Outcome: OutcomeFailed,
				Reason:  FailureReasonProtocolError,
				Message: "Node returned an unsupported terminal outcome",
			}, now)
		}
		changed = true
	default:
		setTerminal(updated, &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonProtocolError,
			Message: "Node returned an unsupported Operation status",
		}, now)
		changed = true
	}

	if updated.Status == StatusStopping &&
		(operation.Status == nodeapi.OperationStatusPending ||
			operation.Status == nodeapi.OperationStatusRunning) {
		if !now.Before(updated.StopDeadline) {
			setTerminal(updated, &TerminalResult{
				Outcome: OutcomeFailed,
				Reason:  FailureReasonStopTimeout,
				Message: "Node Operation did not stop before the Job stop deadline",
			}, now)
			changed = true
		} else {
			shouldStop = true
		}
	}

	runtime.mu.Unlock()
	if changed {
		if err := m.persistRuntime(ctx, runtime, current, updated); err != nil {
			if errors.Is(err, ErrConflict) {
				return false, false, fmt.Errorf("%w: reconcile Job %q: %w", ErrConflict, current.ID, err)
			}
			return false, false, fmt.Errorf(
				"%w: reconcile Job %q: %w",
				ErrPersistence,
				current.ID,
				err,
			)
		}
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
	current := runtime.job
	if isTerminal(current.Status) {
		runtime.mu.Unlock()
		return true, nil
	}
	now := m.now()
	updated := cloneJob(current)

	if isOperationNotFound(err) {
		if runtime.operationObserved {
			setTerminal(updated, &TerminalResult{
				Outcome: OutcomeFailed,
				Reason:  FailureReasonOperationLost,
				Message: "Node no longer has the previously observed Operation",
			}, now)
		} else {
			setTerminal(updated, &TerminalResult{Outcome: OutcomeUnknown}, now)
		}
	} else if failure := explicitNodeFailure(err, duringStart); failure != nil {
		setTerminal(updated, failure, now)
	} else if isRecoverableNodeError(err) {
		if current.NodeUnavailableDeadline.IsZero() {
			updated.NodeUnavailableDeadline = now.Add(m.config.NodeUnavailableGracePeriod)
			updated.UpdatedAt = now
		} else if !now.Before(current.NodeUnavailableDeadline) {
			setTerminal(updated, &TerminalResult{
				Outcome: OutcomeFailed,
				Reason:  FailureReasonNodeUnavailable,
				Message: "Node remained unavailable beyond the recovery window",
			}, now)
		} else {
			runtime.mu.Unlock()
			return false, nil
		}
	} else {
		setTerminal(updated, &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonProtocolError,
			Message: "Node response violated the Operation protocol",
		}, now)
	}

	runtime.mu.Unlock()
	if err := m.persistRuntime(ctx, runtime, current, updated); err != nil {
		if errors.Is(err, ErrConflict) {
			return false, fmt.Errorf("%w: persist Node error for Job %q: %w", ErrConflict, current.ID, err)
		}
		return false, fmt.Errorf(
			"%w: persist Node error for Job %q: %w",
			ErrPersistence,
			current.ID,
			err,
		)
	}
	return isTerminal(updated.Status), nil
}

func (m *Manager) persistRuntime(
	ctx context.Context,
	runtime *managedJob,
	current *Job,
	updated *Job,
) error {
	if err := m.store.Save(ctx, updated, current.Status); err != nil {
		m.persistenceFailures.Add(1)
		return err
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.job != current {
		return ErrConflict
	}
	runtime.job = updated
	return nil
}

func (m *Manager) nextSupervisorDelay(runtime *managedJob, superviseErr error) time.Duration {
	if superviseErr != nil {
		return m.config.StatusPollInterval
	}

	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	current := runtime.job
	if isTerminal(current.Status) {
		return 0
	}
	now := m.now()
	next := now.Add(m.config.StatusPollInterval)
	deadlines := []time.Time{current.NodeUnavailableDeadline}
	if current.NodeUnavailableDeadline.IsZero() {
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

func waitForSupervisor(ctx context.Context, wake <-chan struct{}, delay time.Duration) bool {
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-wake:
		return true
	case <-ctx.Done():
		return false
	}
}

func wakeSupervisor(runtime *managedJob) {
	select {
	case runtime.wake <- struct{}{}:
	default:
	}
}

func recoveredPollJitter(jobID string, interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(jobID))
	return time.Duration(hasher.Sum64() % uint64(interval))
}

func explicitNodeFailure(err error, duringStart bool) *TerminalResult {
	var nodeErr *nodeclient.Error
	if !errors.As(err, &nodeErr) {
		if errors.Is(err, nodeclient.ErrProtocol) || errors.Is(err, nodeclient.ErrInvalidArgument) {
			return &TerminalResult{
				Outcome: OutcomeFailed,
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
		return &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonExecutionCapacityExceeded,
			Message: nodeErr.Message,
		}
	case nodeapi.ErrorCodeExecutionEnvironmentUnsupported,
		nodeapi.ErrorCodeServiceNotImplemented,
		nodeapi.ErrorCodeExecutionStartFailed,
		nodeapi.ErrorCodeLaunchTimeout:
		return &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonExecutionStartFailed,
			Message: nodeErr.Message,
		}
	default:
		return &TerminalResult{
			Outcome: OutcomeFailed,
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

func mapOperationFailure(failure *nodeapi.OperationTerminal) *TerminalResult {
	if failure == nil || failure.Reason == nil || *failure.Reason == "" {
		return &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonProtocolError,
			Message: "failed Node Operation did not include a failure",
		}
	}
	reason := FailureReasonProtocolError
	reasonCode := *failure.Reason
	message := ""
	if failure.Message != nil {
		message = *failure.Message
	}
	switch reasonCode {
	case string(nodeapi.ErrorCodeExecutionStartFailed), string(nodeapi.ErrorCodeLaunchTimeout):
		reason = FailureReasonExecutionStartFailed
	case string(nodeapi.ErrorCodeExecutionFailed),
		string(nodeapi.ErrorCodeExecutionStopFailed),
		string(nodeapi.ErrorCodeFinalizationFailed),
		string(nodeapi.ErrorCodeFinalizationTimeout):
		reason = FailureReasonExecutionFailed
	}
	return &TerminalResult{Outcome: OutcomeFailed, Reason: reason, Message: message}
}

func stoppedJobOutcome(reason StopReason) *TerminalResult {
	switch reason {
	case StopReasonUser:
		return &TerminalResult{Outcome: OutcomeStopped}
	case StopReasonStartTimeout:
		return &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonStartTimeout,
			Message: "Node Operation did not start before the Job pending deadline",
		}
	case StopReasonExecutionTimeout:
		return &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonExecutionTimedOut,
			Message: "Node Operation exceeded the Job execution deadline",
		}
	default:
		return &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonExecutionFailed,
			Message: "Node Operation stopped without a persisted Job stop reason",
		}
	}
}

func setStopping(job *Job, reason StopReason, now time.Time, gracePeriod time.Duration) {
	job.Status = StatusStopping
	job.StopReason = reason
	job.StopDeadline = now.Add(gracePeriod)
	job.UpdatedAt = now
}

func setTerminal(
	job *Job,
	terminal *TerminalResult,
	now time.Time,
) {
	job.Status = StatusTerminal
	job.Terminal = terminal
	job.UpdatedAt = now
	if job.EndedAt.IsZero() {
		job.EndedAt = now
	}
}

func clearNodeUnavailable(job *Job) bool {
	if job.NodeUnavailableDeadline.IsZero() {
		return false
	}
	job.NodeUnavailableDeadline = time.Time{}
	return true
}

func runtimeSnapshot(runtime *managedJob) *Job {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	return cloneJob(runtime.job)
}

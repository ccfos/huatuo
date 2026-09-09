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
	"time"

	nodeapi "huatuo-bamai/apis/v1/node"
	"huatuo-bamai/client"
)

func (r *runtime) startOperation(ctx context.Context) (*nodeapi.Operation, error) {
	job, err := r.saveOperationStartDeadline(ctx)
	if err != nil {
		return nil, err
	}
	request, err := buildStartOperationRequest(job)
	if err != nil {
		return nil, &client.NodeError{
			Code:    client.NodeErrorCodeInvalidArgument,
			Message: fmt.Sprintf("build Node Operation start request: %v", err),
		}
	}
	return r.dependencies.nodeClient.StartOperation(ctx, job.Hostname, request)
}

func (r *runtime) saveOperationStartDeadline(ctx context.Context) (*Job, error) {
	if err := r.acquireTransition(ctx); err != nil {
		return nil, err
	}
	defer r.releaseTransition()

	r.mu.Lock()
	current := r.job
	if current.Status != StatusPending || !current.PendingDeadline.IsZero() {
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: Job %q cannot start an Operation", ErrConflict, current.ID)
	}
	now := r.dependencies.now()
	updated := cloneJob(current)
	updated.PendingDeadline = now.Add(r.dependencies.policy.pendingTimeout)
	updated.UpdatedAt = now
	r.mu.Unlock()
	if err := r.saveTransition(ctx, updated); err != nil {
		if errors.Is(err, ErrConflict) {
			return nil, fmt.Errorf(
				"%w: persist Operation start deadline for Job %q: %w",
				ErrConflict,
				current.ID,
				err,
			)
		}
		return nil, fmt.Errorf(
			"%w: persist Operation start deadline for Job %q: %w",
			ErrPersistence,
			current.ID,
			err,
		)
	}
	return updated, nil
}

func (r *runtime) reconcileOperation(
	ctx context.Context,
	operation *nodeapi.Operation,
) (bool, error) {
	terminal, shouldStop, err := r.reconcileJobWithOperation(ctx, operation)
	if err != nil || terminal || !shouldStop {
		return terminal, err
	}

	snapshot := r.snapshot()
	stoppedOperation, err := r.dependencies.nodeClient.StopOperation(
		ctx,
		snapshot.Hostname,
		snapshot.ID,
	)
	if err != nil {
		return r.reconcileJobWithError(ctx, err)
	}
	terminal, _, err = r.reconcileJobWithOperation(ctx, stoppedOperation)
	return terminal, err
}

// reconcileJobWithOperation keeps successful Node snapshots as the only source
// for normal Job lifecycle transitions.
func (r *runtime) reconcileJobWithOperation(
	ctx context.Context,
	operation *nodeapi.Operation,
) (terminal, shouldStop bool, err error) {
	if err := r.acquireTransition(ctx); err != nil {
		return false, false, err
	}
	defer r.releaseTransition()

	r.mu.Lock()
	current := r.job
	if isTerminal(current.Status) {
		r.mu.Unlock()
		return true, false, nil
	}
	now := r.dependencies.now()
	updated := cloneJob(current)
	changed := !updated.NodeUnavailableDeadline.IsZero()
	if changed {
		updated.NodeUnavailableDeadline = time.Time{}
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
			setStopping(
				updated,
				StopReasonStartTimeout,
				now,
				r.dependencies.policy.completionGracePeriod,
			)
			changed = true
		}
	case nodeapi.OperationStatusRunning:
		if current.Status == StatusPending {
			updated.Status = StatusRunning
			updated.StartedAt = now
			updated.ExecutionDeadline = now.Add(current.Duration).Add(
				r.dependencies.policy.completionGracePeriod,
			)
			updated.UpdatedAt = now
			changed = true
		} else if current.Status == StatusRunning && !now.Before(current.ExecutionDeadline) {
			setStopping(
				updated,
				StopReasonExecutionTimeout,
				now,
				r.dependencies.policy.completionGracePeriod,
			)
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
			setTerminal(updated, terminalResultForStop(current.StopReason), now)
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

	r.mu.Unlock()
	if changed {
		if err := r.saveTransition(ctx, updated); err != nil {
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

func terminalResultForStop(reason StopReason) *TerminalResult {
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

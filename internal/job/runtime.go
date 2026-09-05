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
	"sync"
	"sync/atomic"
	"time"

	nodeapi "huatuo-bamai/apis/v1/node"
	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/nodeclient"
)

type runtimePolicy struct {
	statusPollInterval         time.Duration
	pendingTimeout             time.Duration
	completionGracePeriod      time.Duration
	nodeUnavailableGracePeriod time.Duration
}

type runtimeDependencies struct {
	store               Store
	nodeClient          NodeClient
	policy              runtimePolicy
	now                 func() time.Time
	persistenceFailures *atomic.Uint64
}

type runtime struct {
	mu sync.Mutex

	id                string
	kind              Kind
	hostname          string
	job               *Job
	operationObserved bool
	wakeCh            chan struct{}
	recovered         bool
	cancel            context.CancelFunc
	dependencies      *runtimeDependencies
}

func newRuntime(
	job *Job,
	recovered bool,
	cancel context.CancelFunc,
	dependencies *runtimeDependencies,
) *runtime {
	return &runtime{
		id:           job.ID,
		kind:         job.Kind,
		hostname:     job.Hostname,
		job:          cloneJob(job),
		wakeCh:       make(chan struct{}, 1),
		recovered:    recovered,
		cancel:       cancel,
		dependencies: dependencies,
	}
}

func (r *runtime) run(ctx context.Context) {
	if r.recovered {
		if !r.wait(ctx, recoveryStartDelay(
			r.id,
			r.dependencies.policy.statusPollInterval,
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
		terminal, err := r.superviseOnce(ctx)
		if errors.Is(err, ErrConflict) {
			terminal, err = r.reloadFromStore(ctx)
		}
		if err != nil && ctx.Err() == nil {
			log.WithError(err).WithField("job_id", r.id).
				Error("failed to supervise Job")
		}
		if terminal {
			return
		}
		if !r.wait(ctx, r.nextPollDelay(err)) {
			return
		}
	}
}

func (r *runtime) superviseOnce(ctx context.Context) (bool, error) {
	r.mu.Lock()
	if isTerminal(r.job.Status) {
		r.mu.Unlock()
		return true, nil
	}
	shouldStart := r.job.Status == StatusPending && r.job.PendingDeadline.IsZero()
	r.mu.Unlock()

	var (
		operation *nodeapi.Operation
		err       error
	)
	if shouldStart {
		operation, err = r.startOperation(ctx)
		if err != nil {
			if errors.Is(err, ErrPersistence) || errors.Is(err, ErrConflict) {
				return false, err
			}
			return r.handleNodeError(ctx, err, true)
		}
	} else {
		snapshot := r.snapshot()
		operation, err = r.dependencies.nodeClient.GetOperation(ctx, snapshot.Hostname, snapshot.ID)
		if err != nil {
			return r.handleNodeError(ctx, err, false)
		}
	}

	terminal, reconcileErr := r.reconcileOperation(ctx, operation)
	if errors.Is(reconcileErr, nodeclient.ErrProtocol) {
		return r.handleNodeError(ctx, reconcileErr, false)
	}
	return terminal, reconcileErr
}

func (r *runtime) reloadFromStore(ctx context.Context) (bool, error) {
	r.mu.Lock()
	current := r.job
	r.mu.Unlock()

	stored, err := r.dependencies.store.Get(ctx, r.id)
	if err != nil {
		return false, fmt.Errorf("%w: reload Job %q: %w", ErrPersistence, r.id, err)
	}
	if stored.ID != r.id || stored.Kind != r.kind || stored.Hostname != r.hostname {
		return false, fmt.Errorf(
			"%w: reload Job %q: immutable identity changed",
			ErrPersistence,
			r.id,
		)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.job != current {
		return isTerminal(r.job.Status), nil
	}
	r.job = stored
	return isTerminal(stored.Status), nil
}

func (r *runtime) startOperation(ctx context.Context) (*nodeapi.Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	r.mu.Lock()
	current := r.job
	if current.Status != StatusPending || !current.PendingDeadline.IsZero() {
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: Job %q cannot be dispatched", ErrConflict, current.ID)
	}
	now := r.dependencies.now()
	updated := cloneJob(current)
	updated.PendingDeadline = now.Add(r.dependencies.policy.pendingTimeout)
	updated.UpdatedAt = now
	r.mu.Unlock()
	if err := r.saveTransition(ctx, current, updated); err != nil {
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
	return r.startNodeOperation(ctx, updated)
}

func (r *runtime) reconcileOperation(
	ctx context.Context,
	operation *nodeapi.Operation,
) (bool, error) {
	terminal, shouldStop, err := r.applyOperation(ctx, operation)
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
		return r.handleNodeError(ctx, err, false)
	}
	terminal, _, err = r.applyOperation(ctx, stoppedOperation)
	return terminal, err
}

func (r *runtime) applyOperation(
	ctx context.Context,
	operation *nodeapi.Operation,
) (terminal, shouldStop bool, err error) {
	if operation == nil {
		return false, false, fmt.Errorf("%w: Node returned a nil Operation", nodeclient.ErrProtocol)
	}

	r.mu.Lock()
	current := r.job
	if isTerminal(current.Status) {
		r.mu.Unlock()
		return true, false, nil
	}
	r.operationObserved = true

	now := r.dependencies.now()
	updated := cloneJob(current)
	changed := clearNodeUnavailableDeadline(updated)
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
		if err := r.saveTransition(ctx, current, updated); err != nil {
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

func (r *runtime) handleNodeError(
	ctx context.Context,
	err error,
	duringStart bool,
) (bool, error) {
	if err == nil {
		return false, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	r.mu.Lock()
	current := r.job
	if isTerminal(current.Status) {
		r.mu.Unlock()
		return true, nil
	}
	now := r.dependencies.now()
	updated := cloneJob(current)

	if isNodeOperationNotFound(err) {
		if r.operationObserved {
			setTerminal(updated, &TerminalResult{
				Outcome: OutcomeFailed,
				Reason:  FailureReasonOperationLost,
				Message: "Node no longer has the previously observed Operation",
			}, now)
		} else {
			setTerminal(updated, &TerminalResult{Outcome: OutcomeUnknown}, now)
		}
	} else if failure := terminalResultForNodeError(err, duringStart); failure != nil {
		setTerminal(updated, failure, now)
	} else if isRecoverableNodeError(err) {
		if current.NodeUnavailableDeadline.IsZero() {
			updated.NodeUnavailableDeadline = now.Add(
				r.dependencies.policy.nodeUnavailableGracePeriod,
			)
			updated.UpdatedAt = now
		} else if !now.Before(current.NodeUnavailableDeadline) {
			setTerminal(updated, &TerminalResult{
				Outcome: OutcomeFailed,
				Reason:  FailureReasonNodeUnavailable,
				Message: "Node remained unavailable beyond the recovery window",
			}, now)
		} else {
			r.mu.Unlock()
			return false, nil
		}
	} else {
		setTerminal(updated, &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonProtocolError,
			Message: "Node response violated the Operation protocol",
		}, now)
	}

	r.mu.Unlock()
	if err := r.saveTransition(ctx, current, updated); err != nil {
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

func (r *runtime) saveTransition(
	ctx context.Context,
	current *Job,
	updated *Job,
) error {
	if err := r.dependencies.store.Save(ctx, updated, current.Status); err != nil {
		r.dependencies.persistenceFailures.Add(1)
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.job != current {
		return ErrConflict
	}
	r.job = updated
	return nil
}

func (r *runtime) nextPollDelay(superviseErr error) time.Duration {
	if superviseErr != nil {
		return r.dependencies.policy.statusPollInterval
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.job
	if isTerminal(current.Status) {
		return 0
	}
	now := r.dependencies.now()
	next := now.Add(r.dependencies.policy.statusPollInterval)
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

func (r *runtime) wait(ctx context.Context, delay time.Duration) bool {
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-r.wakeCh:
		return true
	case <-ctx.Done():
		return false
	}
}

func (r *runtime) wake() {
	select {
	case r.wakeCh <- struct{}{}:
	default:
	}
}

func recoveryStartDelay(jobID string, interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(jobID))
	return time.Duration(hasher.Sum64() % uint64(interval))
}

func terminalResultForNodeError(err error, duringStart bool) *TerminalResult {
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

func isNodeOperationNotFound(err error) bool {
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

func clearNodeUnavailableDeadline(job *Job) bool {
	if job.NodeUnavailableDeadline.IsZero() {
		return false
	}
	job.NodeUnavailableDeadline = time.Time{}
	return true
}

func (r *runtime) snapshot() *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneJob(r.job)
}

func (r *runtime) status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.job.Status
}

func (r *runtime) stop(ctx context.Context) (*Job, error) {
	r.mu.Lock()
	current := r.job
	if isTerminal(current.Status) {
		r.mu.Unlock()
		return nil, ErrJobTerminal
	}
	if current.Status == StatusStopping {
		result := cloneJob(current)
		r.mu.Unlock()
		return result, nil
	}

	now := r.dependencies.now()
	updated := cloneJob(current)
	if current.Status == StatusPending && current.PendingDeadline.IsZero() {
		updated.StopReason = StopReasonUser
		setTerminal(updated, &TerminalResult{Outcome: OutcomeStopped}, now)
	} else {
		setStopping(
			updated,
			StopReasonUser,
			now,
			r.dependencies.policy.completionGracePeriod,
		)
	}
	r.mu.Unlock()
	if err := r.saveTransition(ctx, current, updated); err != nil {
		if errors.Is(err, ErrConflict) {
			return nil, fmt.Errorf("%w: persist stop for Job %q: %w", ErrConflict, r.id, err)
		}
		return nil, fmt.Errorf("%w: persist stop for Job %q: %w", ErrPersistence, r.id, err)
	}
	r.wake()
	return cloneJob(updated), nil
}

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

	apiv1 "huatuo-bamai/apis/v1"
	nodeapi "huatuo-bamai/apis/v1/node"
	"huatuo-bamai/client"
)

// reconcileJobWithError keeps failure transitions independent of the Node
// method and HTTP status by relying on stable error codes.
func (r *runtime) reconcileJobWithError(
	ctx context.Context,
	err error,
) (bool, error) {
	if err == nil {
		return false, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	var nodeErr *client.NodeError
	if !errors.As(err, &nodeErr) || nodeErr == nil {
		return false, fmt.Errorf(
			"reconcile Node error for Job %q: expected *client.NodeError: %w",
			r.id,
			err,
		)
	}
	if transitionErr := r.acquireTransition(ctx); transitionErr != nil {
		return false, transitionErr
	}
	defer r.releaseTransition()

	r.mu.Lock()
	current := r.job
	if isTerminal(current.Status) {
		r.mu.Unlock()
		return true, nil
	}
	now := r.dependencies.now()
	updated := cloneJob(current)

	nodeUnavailable := false
	switch nodeErr.Code {
	case nodeapi.ErrorCodeOperationNotFound:
		// StartedAt is durable evidence that Node previously returned a running Operation.
		if !current.StartedAt.IsZero() {
			setTerminal(updated, &TerminalResult{
				Outcome: OutcomeFailed,
				Reason:  FailureReasonOperationLost,
				Message: "Node no longer has the previously observed Operation",
			}, now)
		} else {
			setTerminal(updated, &TerminalResult{Outcome: OutcomeUnknown}, now)
		}
	case client.NodeErrorCodeInvalidArgument,
		apiv1.ErrorCodeInvalidRequest,
		nodeapi.ErrorCodeRequestIDConflict:
		setTerminal(updated, &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonInvalidNodeRequest,
			Message: nodeErr.Message,
		}, now)
	case client.NodeErrorCodeProtocol:
		setTerminal(updated, &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonProtocolError,
			Message: "Node response violated the Operation protocol",
		}, now)
	case client.NodeErrorCodeTransport,
		apiv1.ErrorCodeInternal,
		apiv1.ErrorCodeServiceUnavailable:
		nodeUnavailable = true
	case nodeapi.ErrorCodeOperationLimitExceeded:
		setTerminal(updated, &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonExecutionCapacityExceeded,
			Message: nodeErr.Message,
		}, now)
	case nodeapi.ErrorCodeExecutionEnvironmentUnsupported,
		nodeapi.ErrorCodeServiceNotImplemented,
		nodeapi.ErrorCodeExecutionStartFailed,
		nodeapi.ErrorCodeLaunchTimeout:
		setTerminal(updated, &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonExecutionStartFailed,
			Message: nodeErr.Message,
		}, now)
	case nodeapi.ErrorCodeExecutionFailed,
		nodeapi.ErrorCodeExecutionStopFailed,
		nodeapi.ErrorCodeFinalizationFailed,
		nodeapi.ErrorCodeFinalizationTimeout:
		setTerminal(updated, &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonExecutionFailed,
			Message: nodeErr.Message,
		}, now)
	default:
		setTerminal(updated, &TerminalResult{
			Outcome: OutcomeFailed,
			Reason:  FailureReasonProtocolError,
			Message: "Node response violated the Operation protocol",
		}, now)
	}

	if nodeUnavailable {
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
	}

	r.mu.Unlock()
	if err := r.saveTransition(ctx, updated); err != nil {
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

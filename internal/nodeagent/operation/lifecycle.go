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

package operation

import (
	"context"
	"errors"

	"huatuo-bamai/internal/log"
)

const (
	messageExecutionStartFailed = "execution did not start"
	messageLaunchTimeout        = "execution launch timed out"
	messageExecutionFailed      = "execution failed"
	messageExecutionStopFailed  = "execution could not be stopped"
	messageFinalizationFailed   = "execution finalization failed"
	messageFinalizationTimeout  = "execution finalization timed out"
)

type lifecycleFailure struct {
	reason  FailureReason
	message string
	phase   string
	err     error
}

func (m *Manager) runOperation(managed *managedOperation) {
	defer m.wg.Done()

	runtime := managed.execution
	lifecycleCtx := context.Background()
	launchCtx, cancelLaunch := context.WithTimeout(lifecycleCtx, m.lifecycle.LaunchTimeout)
	m.mu.Lock()
	runtime.launchCancel = cancelLaunch
	stopRequested := managed.state.Status == StatusStopping
	m.mu.Unlock()
	if stopRequested {
		cancelLaunch()
	}

	startErr := runtime.executor.Start(launchCtx)
	launchContextErr := launchCtx.Err()
	cancelLaunch()
	if startErr != nil {
		m.finishStartFailure(managed, runtime, startErr, launchContextErr)
		return
	}

	m.mu.Lock()
	runtime.launchCancel = nil
	startedAt := m.now()
	managed.state.StartedAt = &startedAt
	if managed.state.Status == StatusPending {
		managed.state.Status = StatusRunning
	}
	var action *stopAction
	if managed.state.Status == StatusStopping {
		action = m.prepareStopLocked(managed)
	}
	m.mu.Unlock()
	m.startStop(action)

	waitErr := runtime.executor.Wait()
	m.mu.Lock()
	managed.isFinalizing = true
	stopRequested = managed.state.Status == StatusStopping
	stopStarted := runtime.isStopStarted
	m.mu.Unlock()

	var stopErr error
	if stopStarted {
		stopErr = <-runtime.stopResultCh
	}

	mode := FinalizeDiscard
	if !stopRequested && waitErr == nil {
		mode = FinalizePublish
	}
	finalizeCtx, cancelFinalize := context.WithTimeout(
		lifecycleCtx,
		m.lifecycle.FinalizationTimeout,
	)
	finalizeErr := runtime.executor.Finalize(finalizeCtx, mode)
	finalizeContextErr := finalizeCtx.Err()
	cancelFinalize()

	failure, terminalOutcome := classifyExecution(stopRequested, waitErr, stopErr)
	finalizeFailure := classifyFinalization(finalizeErr, finalizeContextErr)
	if failure == nil {
		failure = finalizeFailure
	} else if finalizeFailure != nil {
		failure.err = errors.Join(failure.err, finalizeFailure.err)
	}
	if failure != nil {
		terminalOutcome = OutcomeFailed
	}
	m.finishOperation(managed, runtime, terminalOutcome, failure)
}

func (m *Manager) finishStartFailure(
	managed *managedOperation,
	runtime *executionRuntime,
	startErr error,
	launchContextErr error,
) {
	m.mu.Lock()
	runtime.launchCancel = nil
	stopRequested := managed.state.Status == StatusStopping

	if stopRequested && errors.Is(startErr, context.Canceled) &&
		!errors.Is(launchContextErr, context.DeadlineExceeded) {
		m.commitTerminalLocked(managed, runtime, OutcomeStopped, nil)
		m.mu.Unlock()
		return
	}

	failure := &lifecycleFailure{
		reason:  FailureReasonExecutionStartFailed,
		message: messageExecutionStartFailed,
		phase:   "start",
		err:     startErr,
	}
	if errors.Is(startErr, context.DeadlineExceeded) ||
		errors.Is(launchContextErr, context.DeadlineExceeded) {
		failure.reason = FailureReasonLaunchTimeout
		failure.message = messageLaunchTimeout
		failure.phase = "launch"
	}
	m.commitTerminalLocked(managed, runtime, OutcomeFailed, failure)
	requestID := managed.state.RequestID
	kind := managed.state.Kind
	m.mu.Unlock()
	m.logFailure(requestID, kind, failure)
}

func classifyExecution(stopRequested bool, waitErr, stopErr error) (*lifecycleFailure, Outcome) {
	if stopRequested {
		if stopErr != nil {
			if waitErr != nil && !errors.Is(waitErr, ErrStopped) {
				stopErr = errors.Join(stopErr, waitErr)
			}
			return &lifecycleFailure{
				reason:  FailureReasonExecutionStopFailed,
				message: messageExecutionStopFailed,
				phase:   "stop",
				err:     stopErr,
			}, OutcomeFailed
		}
		// Tools may translate SIGTERM into their own non-zero exit code. Once
		// Stop succeeds, that code cannot make the discarded result meaningful.
		return nil, OutcomeStopped
	}
	if waitErr != nil {
		return &lifecycleFailure{
			reason:  FailureReasonExecutionFailed,
			message: messageExecutionFailed,
			phase:   "wait",
			err:     waitErr,
		}, OutcomeFailed
	}
	return nil, OutcomeCompleted
}

func classifyFinalization(finalizeErr, finalizeContextErr error) *lifecycleFailure {
	if finalizeErr == nil {
		return nil
	}
	failure := &lifecycleFailure{
		reason:  FailureReasonFinalizationFailed,
		message: messageFinalizationFailed,
		phase:   "finalize",
		err:     finalizeErr,
	}
	if errors.Is(finalizeErr, context.DeadlineExceeded) ||
		errors.Is(finalizeContextErr, context.DeadlineExceeded) {
		failure.reason = FailureReasonFinalizationTimeout
		failure.message = messageFinalizationTimeout
	}
	return failure
}

func (m *Manager) finishOperation(
	managed *managedOperation,
	runtime *executionRuntime,
	outcome Outcome,
	failure *lifecycleFailure,
) {
	m.mu.Lock()
	m.commitTerminalLocked(managed, runtime, outcome, failure)
	requestID := managed.state.RequestID
	kind := managed.state.Kind
	m.mu.Unlock()
	if failure != nil {
		m.logFailure(requestID, kind, failure)
	}
}

func (m *Manager) commitTerminalLocked(
	managed *managedOperation,
	runtime *executionRuntime,
	outcome Outcome,
	failure *lifecycleFailure,
) {
	if isTerminal(managed.state.Status) {
		return
	}

	finishedAt := m.now()
	managed.state.Status = StatusTerminal
	managed.state.FinishedAt = &finishedAt
	managed.state.Terminal = &TerminalResult{Outcome: outcome}
	if failure != nil {
		managed.state.Terminal = &TerminalResult{
			Outcome: outcome,
			Reason:  failure.reason,
			Message: failure.message,
		}
	}
	managed.isFinalizing = false
	managed.expiresAt = finishedAt.Add(m.lifecycle.TerminalRetentionPeriod)
	managed.execution = nil
	m.activeCount--
}

func (m *Manager) logFailure(requestID string, kind Kind, failure *lifecycleFailure) {
	log.WithError(failure.err).
		WithField("request_id", requestID).
		WithField("kind", kind).
		WithField("phase", failure.phase).
		Error("operation lifecycle failed")
}

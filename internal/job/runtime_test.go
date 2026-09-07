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
	"sync/atomic"
	"testing"
	"time"

	apiv1 "huatuo-bamai/apis/v1"
	nodeapi "huatuo-bamai/apis/v1/node"
	"huatuo-bamai/internal/nodeclient"
)

func TestRuntimeStartOperationReturnsContextCancellation(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	pendingJob := testJob("job-1", StatusPending, now)
	manager := testManager(newMemoryStore(pendingJob), &stubNodeClient{})
	runtime := testRuntime(manager, pendingJob)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := runtime.startOperation(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("startOperation() error = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrShuttingDown) {
		t.Fatalf("startOperation() error = %v, must not be ErrShuttingDown", err)
	}
}

func TestRuntimeStartOperationClassifiesRequestBuildError(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	pendingJob := testJob("job-1", StatusPending, now)
	pendingJob.Kind = Kind("unsupported")
	runtime := testRuntime(
		testManager(newMemoryStore(pendingJob), &stubNodeClient{}),
		pendingJob,
	)

	_, err := runtime.startOperation(t.Context())
	var nodeErr *nodeclient.Error
	if !errors.As(err, &nodeErr) || nodeErr.Code != nodeclient.ErrorCodeClientInvalidArgument {
		t.Fatalf("startOperation() error = %v, want client_invalid_argument", err)
	}
}

func TestRuntimeStartOperationPersistsDeadlineBeforeNodeCall(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	pendingJob := testJob("job-1", StatusPending, now)
	store := newMemoryStore(pendingJob)
	manager := testManager(store, nil)
	setManagerNow(manager, func() time.Time { return now })
	var deadlinePersisted atomic.Bool
	store.saveHook = func(saved *Job, expectedRevision int64) error {
		if expectedRevision != 1 {
			t.Fatalf("Save() expected revision = %d, want 1", expectedRevision)
		}
		if saved.revision != 2 {
			t.Fatalf("Save() revision = %d, want 2", saved.revision)
		}
		if saved.PendingDeadline.IsZero() {
			t.Fatal("Save() did not contain the Operation start deadline")
		}
		deadlinePersisted.Store(true)
		return nil
	}
	manager.runtimeDeps.nodeClient = &stubNodeClient{startOperation: func(
		_ context.Context,
		_ string,
		request *nodeapi.StartOperationRequest,
	) (*nodeapi.Operation, error) {
		if !deadlinePersisted.Load() {
			t.Fatal("StartOperation() ran before the Operation start deadline was durable")
		}
		return operation(request.RequestID, nodeapi.OperationStatusPending), nil
	}}
	runtime := testRuntime(manager, pendingJob)

	got, err := runtime.startOperation(t.Context())
	if err != nil {
		t.Fatalf("startOperation() error = %v", err)
	}
	if got.RequestID != pendingJob.ID {
		t.Fatalf("startOperation() request ID = %q, want %q", got.RequestID, pendingJob.ID)
	}
}

func TestRuntimeStopReadsStateAfterCurrentTransition(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	pendingJob := testJob("job-1", StatusPending, now)
	manager := testManager(newMemoryStore(pendingJob), &stubNodeClient{})
	setManagerNow(manager, func() time.Time { return now.Add(time.Second) })
	runtime := testRuntime(manager, pendingJob)

	if err := runtime.acquireTransition(t.Context()); err != nil {
		t.Fatalf("acquireTransition() error = %v", err)
	}
	result := make(chan struct {
		job *Job
		err error
	}, 1)
	go func() {
		job, err := runtime.stop(t.Context())
		result <- struct {
			job *Job
			err error
		}{job: job, err: err}
	}()

	runtime.mu.Lock()
	current := runtime.job
	updated := cloneJob(current)
	updated.PendingDeadline = now.Add(time.Minute)
	runtime.mu.Unlock()
	if err := runtime.saveTransition(t.Context(), updated); err != nil {
		runtime.releaseTransition()
		t.Fatalf("saveTransition() error = %v", err)
	}
	runtime.releaseTransition()

	got := <-result
	if got.err != nil {
		t.Fatalf("stop() error = %v", got.err)
	}
	if got.job.Status != StatusStopping {
		t.Fatalf("stop() status = %q, want %q", got.job.Status, StatusStopping)
	}
}

func TestRuntimeStopHonorsCancellationWhileWaitingForTransition(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	pendingJob := testJob("job-1", StatusPending, now)
	runtime := testRuntime(
		testManager(newMemoryStore(pendingJob), &stubNodeClient{}),
		pendingJob,
	)
	if err := runtime.acquireTransition(t.Context()); err != nil {
		t.Fatalf("acquireTransition() error = %v", err)
	}
	defer runtime.releaseTransition()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := runtime.stop(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("stop() error = %v, want context.Canceled", err)
	}
}

func TestRuntimeDistinguishesUnknownAndLostOperations(t *testing.T) {
	tests := []struct {
		name              string
		operationObserved bool
		wantStatus        Status
		wantReason        FailureReason
	}{
		{
			name:       "start response was never observed",
			wantStatus: StatusTerminal,
		},
		{
			name:              "previously observed operation disappeared",
			operationObserved: true,
			wantStatus:        StatusTerminal,
			wantReason:        FailureReasonOperationLost,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
			pendingJob := testJob("job-1", StatusPending, now)
			store := newMemoryStore(pendingJob)
			manager := testManager(store, &stubNodeClient{})
			setManagerNow(manager, func() time.Time { return now.Add(time.Second) })
			runtime := testRuntime(manager, pendingJob)
			runtime.operationObserved = tt.operationObserved
			nodeErr := &nodeclient.Error{
				StatusCode: 404,
				Code:       nodeapi.ErrorCodeOperationNotFound,
				Message:    "operation not found",
			}

			terminal, err := runtime.reconcileJobWithNodeError(t.Context(), nodeErr)
			if err != nil || !terminal {
				t.Fatalf("reconcileJobWithNodeError() = (%t, %v)", terminal, err)
			}
			got := runtime.snapshot()
			if got.Status != StatusTerminal {
				t.Fatalf("status = %q, want %q", got.Status, tt.wantStatus)
			}
			if !tt.operationObserved && got.Terminal.Outcome != OutcomeUnknown {
				t.Fatalf("outcome = %q, want unknown", got.Terminal.Outcome)
			}
			if tt.operationObserved && got.Terminal.Outcome != OutcomeFailed {
				t.Fatalf("outcome = %q, want failed", got.Terminal.Outcome)
			}
			if tt.wantReason == "" {
			} else if got.Terminal == nil || got.Terminal.Reason != tt.wantReason {
				t.Fatalf("terminal = %+v, want reason %q", got.Terminal, tt.wantReason)
			}
		})
	}
}

func TestRuntimeReconcileJobWithNodeErrorClassifiesErrors(t *testing.T) {
	tests := []struct {
		name                    string
		err                     error
		wantTerminal            bool
		wantStatus              Status
		wantReason              FailureReason
		wantUnavailableDeadline bool
	}{
		{
			name: "start capacity failure",
			err: &nodeclient.Error{
				StatusCode: 429,
				Code:       nodeapi.ErrorCodeOperationLimitExceeded,
				Message:    "profiling capacity exhausted",
			},
			wantTerminal: true,
			wantStatus:   StatusTerminal,
			wantReason:   FailureReasonExecutionCapacityExceeded,
		},
		{
			name: "execution start failure",
			err: &nodeclient.Error{
				StatusCode: 500,
				Code:       nodeapi.ErrorCodeExecutionStartFailed,
				Message:    "invalid operation state",
			},
			wantTerminal: true,
			wantStatus:   StatusTerminal,
			wantReason:   FailureReasonExecutionStartFailed,
		},
		{
			name:         "client protocol error",
			err:          &nodeclient.Error{Code: nodeclient.ErrorCodeClientProtocol},
			wantTerminal: true,
			wantStatus:   StatusTerminal,
			wantReason:   FailureReasonProtocolError,
		},
		{
			name: "client invalid argument",
			err: &nodeclient.Error{
				Code:    nodeclient.ErrorCodeClientInvalidArgument,
				Message: "invalid Node host",
			},
			wantTerminal: true,
			wantStatus:   StatusTerminal,
			wantReason:   FailureReasonInvalidNodeRequest,
		},
		{
			name:                    "client transport error",
			err:                     &nodeclient.Error{Code: nodeclient.ErrorCodeClientTransport},
			wantStatus:              StatusPending,
			wantUnavailableDeadline: true,
		},
		{
			name: "node unavailable",
			err: &nodeclient.Error{
				StatusCode: 503,
				Code:       apiv1.ErrorCodeServiceUnavailable,
				Message:    "service unavailable",
			},
			wantStatus:              StatusPending,
			wantUnavailableDeadline: true,
		},
		{
			name: "execution failure",
			err: &nodeclient.Error{
				StatusCode: 500,
				Code:       nodeapi.ErrorCodeExecutionFailed,
				Message:    "executor failed",
			},
			wantTerminal: true,
			wantStatus:   StatusTerminal,
			wantReason:   FailureReasonExecutionFailed,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
			pendingJob := testJob("job-1", StatusPending, now)
			manager := testManager(newMemoryStore(pendingJob), &stubNodeClient{})
			setManagerNow(manager, func() time.Time { return now })
			runtime := testRuntime(manager, pendingJob)

			terminal, err := runtime.reconcileJobWithNodeError(t.Context(), tt.err)
			if err != nil || terminal != tt.wantTerminal {
				t.Fatalf(
					"reconcileJobWithNodeError() = (%t, %v), want (%t, nil)",
					terminal,
					err,
					tt.wantTerminal,
				)
			}
			got := runtime.snapshot()
			if got.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", got.Status, tt.wantStatus)
			}
			if tt.wantReason != "" && (got.Terminal == nil || got.Terminal.Reason != tt.wantReason) {
				t.Fatalf("terminal = %+v, want reason %q", got.Terminal, tt.wantReason)
			}
			wantDeadline := time.Time{}
			if tt.wantUnavailableDeadline {
				wantDeadline = now.Add(time.Minute)
			}
			if !got.NodeUnavailableDeadline.Equal(wantDeadline) {
				t.Fatalf(
					"NodeUnavailableDeadline = %s, want %s",
					got.NodeUnavailableDeadline,
					wantDeadline,
				)
			}
		})
	}
}

func TestRuntimeReconcileJobWithNodeErrorRejectsUnstructuredError(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	pendingJob := testJob("job-1", StatusPending, now)
	runtime := testRuntime(
		testManager(newMemoryStore(pendingJob), &stubNodeClient{}),
		pendingJob,
	)

	terminal, err := runtime.reconcileJobWithNodeError(
		t.Context(),
		errors.New("Node unavailable"),
	)
	if err == nil || terminal {
		t.Fatalf(
			"reconcileJobWithNodeError() = (%t, %v), want (false, error)",
			terminal,
			err,
		)
	}
	if got := runtime.snapshot(); got.Status != StatusPending ||
		!got.NodeUnavailableDeadline.IsZero() {
		t.Fatalf("Job = (%q, %s), want unchanged pending Job", got.Status, got.NodeUnavailableDeadline)
	}
}

func TestRuntimeExecutionTimeoutStopsThenFailsJob(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	runningJob := testJob("job-1", StatusRunning, now.Add(-2*time.Minute))
	runningJob.PendingDeadline = now.Add(-90 * time.Second)
	runningJob.StartedAt = now.Add(-time.Minute)
	runningJob.ExecutionDeadline = now
	store := newMemoryStore(runningJob)
	var stopCalls atomic.Int32
	client := &stubNodeClient{stopOperation: func(
		_ context.Context,
		_ string,
		requestID string,
	) (*nodeapi.Operation, error) {
		stopCalls.Add(1)
		return terminalOperation(requestID, nodeapi.OperationOutcomeStopped), nil
	}}
	manager := testManager(store, client)
	setManagerNow(manager, func() time.Time { return now })
	runtime := testRuntime(manager, runningJob)

	terminal, err := runtime.reconcileOperation(
		t.Context(),
		operation(runningJob.ID, nodeapi.OperationStatusRunning),
	)
	if err != nil || !terminal {
		t.Fatalf("reconcileOperation() = (%t, %v)", terminal, err)
	}
	got := runtime.snapshot()
	if got.Status != StatusTerminal || got.Terminal == nil || got.Terminal.Outcome != OutcomeFailed ||
		got.Terminal.Reason != FailureReasonExecutionTimedOut {
		t.Fatalf("timed-out Job = (%q, %+v)", got.Status, got.Terminal)
	}
	if stopCalls.Load() != 1 {
		t.Fatalf("StopOperation() calls = %d, want 1", stopCalls.Load())
	}
}

func TestRuntimeNodeUnavailableDoesNotSpinOnBusinessDeadline(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	runningJob := testJob("job-1", StatusRunning, now.Add(-time.Minute))
	runningJob.StartedAt = now.Add(-time.Minute)
	runningJob.ExecutionDeadline = now.Add(-time.Second)
	runningJob.NodeUnavailableDeadline = now.Add(time.Minute)
	manager := testManager(newMemoryStore(runningJob), &stubNodeClient{})
	manager.runtimeDeps.policy.statusPollInterval = 5 * time.Second
	setManagerNow(manager, func() time.Time { return now })
	runtime := testRuntime(manager, runningJob)

	if got := runtime.nextPollDelay(nil); got != 5*time.Second {
		t.Fatalf("nextPollDelay() = %s, want 5s", got)
	}
}

func TestRuntimeSupervisorErrorUsesPollInterval(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	runningJob := testJob("job-1", StatusRunning, now.Add(-time.Minute))
	runningJob.ExecutionDeadline = now.Add(-time.Second)
	manager := testManager(newMemoryStore(runningJob), &stubNodeClient{})
	manager.runtimeDeps.policy.statusPollInterval = 5 * time.Second
	setManagerNow(manager, func() time.Time { return now })
	runtime := testRuntime(manager, runningJob)

	got := runtime.nextPollDelay(ErrPersistence)
	if got != 5*time.Second {
		t.Fatalf("nextPollDelay() = %s, want 5s", got)
	}
}

func TestRuntimeReloadFromStoreUsesPersistedState(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	staleJob := testJob("job-1", StatusRunning, now.Add(-time.Minute))
	persistedJob := cloneJob(staleJob)
	setTerminal(persistedJob, &TerminalResult{Outcome: OutcomeCompleted}, now)
	manager := testManager(newMemoryStore(persistedJob), &stubNodeClient{})
	runtime := testRuntime(manager, staleJob)

	terminal, err := runtime.reloadFromStore(t.Context())
	if err != nil || !terminal {
		t.Fatalf("reloadFromStore() = (%t, %v), want (true, nil)", terminal, err)
	}
	got := runtime.snapshot()
	if got.Status != StatusTerminal || got.Terminal == nil ||
		got.Terminal.Outcome != OutcomeCompleted {
		t.Fatalf("reloaded Job = (%q, %+v), want succeeded terminal Job", got.Status, got.Terminal)
	}
}

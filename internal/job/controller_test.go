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

	nodeapi "huatuo-bamai/apis/v1/node"
	"huatuo-bamai/internal/nodeclient"
)

func TestManagerStartReturnsContextCancellation(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	pendingJob := testJob("job-1", StatusPending, now)
	manager := testManager(newMemoryStore(pendingJob), &stubNodeClient{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := manager.start(ctx, testManagedJob(pendingJob))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("start() error = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrShuttingDown) {
		t.Fatalf("start() error = %v, must not be ErrShuttingDown", err)
	}
}

func TestManagerStartPersistsDispatchMarkerBeforeNodeCall(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	pendingJob := testJob("job-1", StatusPending, now)
	store := newMemoryStore(pendingJob)
	manager := testManager(store, nil)
	manager.now = func() time.Time { return now }
	var markerPersisted atomic.Bool
	store.saveHook = func(saved *Job, expected Status) error {
		if expected != StatusPending {
			t.Fatalf("Save() expected status = %q", expected)
		}
		if saved.PendingDeadline.IsZero() {
			t.Fatal("Save() did not contain the dispatch marker")
		}
		markerPersisted.Store(true)
		return nil
	}
	manager.nodeClient = &stubNodeClient{startOperation: func(
		_ context.Context,
		_ string,
		request *nodeapi.StartOperationRequest,
	) (*nodeapi.Operation, error) {
		if !markerPersisted.Load() {
			t.Fatal("StartOperation() ran before the dispatch marker was durable")
		}
		return operation(request.RequestID, nodeapi.OperationStatusPending), nil
	}}

	got, err := manager.start(t.Context(), testManagedJob(pendingJob))
	if err != nil {
		t.Fatalf("start() error = %v", err)
	}
	if got.RequestID != pendingJob.ID {
		t.Fatalf("start() request ID = %q, want %q", got.RequestID, pendingJob.ID)
	}
}

func TestManagerDistinguishesUnknownAndLostOperations(t *testing.T) {
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
			manager.now = func() time.Time { return now.Add(time.Second) }
			runtime := testManagedJob(pendingJob)
			runtime.operationObserved = tt.operationObserved
			nodeErr := &nodeclient.Error{
				StatusCode: 404,
				Code:       nodeapi.ErrorCodeOperationNotFound,
				Message:    "operation not found",
			}

			terminal, err := manager.handleNodeError(t.Context(), runtime, nodeErr, false)
			if err != nil || !terminal {
				t.Fatalf("handleNodeError() = (%t, %v)", terminal, err)
			}
			got := runtimeSnapshot(runtime)
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

func TestManagerExecutionTimeoutStopsThenFailsJob(t *testing.T) {
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
	manager.now = func() time.Time { return now }
	runtime := testManagedJob(runningJob)

	terminal, err := manager.reconcileAndStop(
		t.Context(),
		runtime,
		operation(runningJob.ID, nodeapi.OperationStatusRunning),
	)
	if err != nil || !terminal {
		t.Fatalf("reconcileAndStop() = (%t, %v)", terminal, err)
	}
	got := runtimeSnapshot(runtime)
	if got.Status != StatusTerminal || got.Terminal == nil || got.Terminal.Outcome != OutcomeFailed ||
		got.Terminal.Reason != FailureReasonExecutionTimedOut {
		t.Fatalf("timed-out Job = (%q, %+v)", got.Status, got.Terminal)
	}
	if stopCalls.Load() != 1 {
		t.Fatalf("StopOperation() calls = %d, want 1", stopCalls.Load())
	}
}

func TestManagerNodeUnavailableDoesNotSpinOnBusinessDeadline(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	runningJob := testJob("job-1", StatusRunning, now.Add(-time.Minute))
	runningJob.StartedAt = now.Add(-time.Minute)
	runningJob.ExecutionDeadline = now.Add(-time.Second)
	runningJob.NodeUnavailableDeadline = now.Add(time.Minute)
	manager := testManager(newMemoryStore(runningJob), &stubNodeClient{})
	manager.config.StatusPollInterval = 5 * time.Second
	manager.now = func() time.Time { return now }

	if got := manager.nextSupervisorDelay(testManagedJob(runningJob), nil); got != 5*time.Second {
		t.Fatalf("nextSupervisorDelay() = %s, want 5s", got)
	}
}

func TestManagerSupervisorErrorUsesPollInterval(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	runningJob := testJob("job-1", StatusRunning, now.Add(-time.Minute))
	runningJob.ExecutionDeadline = now.Add(-time.Second)
	manager := testManager(newMemoryStore(runningJob), &stubNodeClient{})
	manager.config.StatusPollInterval = 5 * time.Second
	manager.now = func() time.Time { return now }

	got := manager.nextSupervisorDelay(testManagedJob(runningJob), ErrPersistence)
	if got != 5*time.Second {
		t.Fatalf("nextSupervisorDelay() = %s, want 5s", got)
	}
}

func TestManagerReloadRuntimeUsesPersistedState(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	staleJob := testJob("job-1", StatusRunning, now.Add(-time.Minute))
	persistedJob := cloneJob(staleJob)
	setTerminal(persistedJob, &TerminalResult{Outcome: OutcomeCompleted}, now)
	manager := testManager(newMemoryStore(persistedJob), &stubNodeClient{})
	runtime := testManagedJob(staleJob)

	terminal, err := manager.reloadRuntime(t.Context(), runtime)
	if err != nil || !terminal {
		t.Fatalf("reloadRuntime() = (%t, %v), want (true, nil)", terminal, err)
	}
	got := runtimeSnapshot(runtime)
	if got.Status != StatusTerminal || got.Terminal == nil ||
		got.Terminal.Outcome != OutcomeCompleted {
		t.Fatalf("reloaded Job = (%q, %+v), want succeeded terminal Job", got.Status, got.Terminal)
	}
}

func TestExplicitNodeCapacityFailure(t *testing.T) {
	failure := explicitNodeFailure(&nodeclient.Error{
		StatusCode: 429,
		Code:       nodeapi.ErrorCodeOperationLimitExceeded,
		Message:    "profiling capacity exhausted",
	}, true)
	if failure == nil || failure.Reason != FailureReasonExecutionCapacityExceeded {
		t.Fatalf("explicitNodeFailure() = %+v", failure)
	}
}

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

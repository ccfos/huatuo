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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nodeapi "huatuo-bamai/apis/v1/node"
	"huatuo-bamai/internal/nodeclient"
	"huatuo-bamai/pkg/observation"
	"huatuo-bamai/pkg/profiling"
)

type memoryStore struct {
	mu sync.Mutex

	jobs     map[string]*Job
	saves    []*Job
	saveHook func(*Job, Status) error
}

func newMemoryStore(jobs ...*Job) *memoryStore {
	store := &memoryStore{jobs: make(map[string]*Job)}
	for _, job := range jobs {
		store.jobs[job.ID] = cloneJob(job)
	}
	return store
}

func (s *memoryStore) Get(_ context.Context, jobID string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	storedJob, ok := s.jobs[jobID]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneJob(storedJob), nil
}

func (s *memoryStore) Create(_ context.Context, job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[job.ID]; ok {
		return ErrAlreadyExists
	}
	s.jobs[job.ID] = cloneJob(job)
	return nil
}

func (s *memoryStore) Save(_ context.Context, job *Job, expected Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.jobs[job.ID]
	if !ok {
		return ErrNotFound
	}
	if current.Status != expected {
		return ErrConflict
	}
	if s.saveHook != nil {
		if err := s.saveHook(cloneJob(job), expected); err != nil {
			return err
		}
	}
	s.jobs[job.ID] = cloneJob(job)
	s.saves = append(s.saves, cloneJob(job))
	return nil
}

func (s *memoryStore) List(_ context.Context, query *Query) ([]*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := make([]*Job, 0, len(s.jobs))
	for _, storedJob := range s.jobs {
		if query != nil && len(query.Statuses) != 0 {
			matched := false
			for _, status := range query.Statuses {
				matched = matched || storedJob.Status == status
			}
			if !matched {
				continue
			}
		}
		jobs = append(jobs, cloneJob(storedJob))
	}
	if query == nil {
		return jobs, nil
	}
	start := min(query.Offset, len(jobs))
	end := len(jobs)
	if query.Limit > 0 {
		end = min(start+query.Limit, end)
	}
	return jobs[start:end], nil
}

func (s *memoryStore) DeleteTerminalBefore(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (s *memoryStore) Ping(context.Context) error {
	return nil
}

func (s *memoryStore) Close(context.Context) error {
	return nil
}

type stubNodeClient struct {
	startOperation func(context.Context, string, *nodeapi.StartOperationRequest) (*nodeapi.Operation, error)
	getOperation   func(context.Context, string, string) (*nodeapi.Operation, error)
	stopOperation  func(context.Context, string, string) (*nodeapi.Operation, error)
}

func (s *stubNodeClient) StartOperation(
	ctx context.Context,
	host string,
	request *nodeapi.StartOperationRequest,
) (*nodeapi.Operation, error) {
	if s.startOperation != nil {
		return s.startOperation(ctx, host, request)
	}
	return operation(request.RequestID, nodeapi.OperationStatusPending), nil
}

func (s *stubNodeClient) GetOperation(
	ctx context.Context,
	host string,
	requestID string,
) (*nodeapi.Operation, error) {
	if s.getOperation != nil {
		return s.getOperation(ctx, host, requestID)
	}
	return operation(requestID, nodeapi.OperationStatusPending), nil
}

func (s *stubNodeClient) StopOperation(
	ctx context.Context,
	host string,
	requestID string,
) (*nodeapi.Operation, error) {
	if s.stopOperation != nil {
		return s.stopOperation(ctx, host, requestID)
	}
	return terminalOperation(requestID, nodeapi.OperationOutcomeStopped), nil
}

func testManager(store Store, client NodeClient) *Manager {
	return newManagerWithStore(store, client, &ManagerConfig{
		ProfilingPolicy:            Policy{MaxJobsPerHost: 2, MaxTotalJobs: 2},
		TracingPolicy:              Policy{MaxJobsPerHost: 2, MaxTotalJobs: 2},
		StatusPollInterval:         time.Hour,
		PendingTimeout:             time.Minute,
		CompletionGracePeriod:      time.Minute,
		NodeUnavailableGracePeriod: time.Minute,
		JobRetentionPeriod:         time.Hour,
	})
}

func testManagedJob(job *Job) *managedJob {
	return newManagedJob(job, false, func() {})
}

func testJob(id string, status Status, now time.Time) *Job {
	job := &Job{
		ID:       id,
		Kind:     KindProfiling,
		UserID:   "user-1",
		Hostname: "node-1",
		Duration: time.Minute,
		Scope:    observation.ScopeHost,
		Spec: Spec{Profiling: &profiling.Spec{
			Type:     profiling.TypeCPU,
			Language: profiling.LanguageGo,
			Mode:     profiling.ModeOnCPU,
		}},
		Status:    status,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if isTerminal(status) {
		job.EndedAt = now
		switch status {
		case Status("completed"):
			job.Terminal = &TerminalResult{Outcome: OutcomeCompleted}
		case StatusTerminal:
			job.Terminal = &TerminalResult{Outcome: OutcomeCompleted}
		case Status("failed"):
			job.Terminal = &TerminalResult{
				Outcome: OutcomeFailed,
				Reason:  FailureReasonExecutionFailed, Message: "failed",
			}
		case Status("stopped"):
			job.Terminal = &TerminalResult{Outcome: OutcomeStopped}
		case Status("outcome_unknown"):
			job.Terminal = &TerminalResult{Outcome: OutcomeUnknown}
		}
	}
	return job
}

func testCreateRequest() *CreateRequest {
	return &CreateRequest{
		UserID:   "user-1",
		Hostname: "node-1",
		Duration: time.Minute,
		Scope:    observation.ScopeHost,
		Spec: Spec{Profiling: &profiling.Spec{
			Type:     profiling.TypeCPU,
			Language: profiling.LanguageGo,
			Mode:     profiling.ModeOnCPU,
		}},
	}
}

func operation(requestID string, status nodeapi.OperationStatus) *nodeapi.Operation {
	return &nodeapi.Operation{
		RequestID: requestID,
		Status:    status,
		CreatedAt: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	}
}

func terminalOperation(requestID string, outcome nodeapi.OperationOutcome) *nodeapi.Operation {
	return &nodeapi.Operation{
		RequestID: requestID,
		Status:    nodeapi.OperationStatusTerminal,
		Terminal:  &nodeapi.OperationTerminal{Outcome: outcome},
		CreatedAt: time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC),
	}
}

func TestNormalizeManagerConfigRequiresBothServicePolicies(t *testing.T) {
	_, err := normalizeManagerConfig(&ManagerConfig{
		ProfilingPolicy: Policy{MaxJobsPerHost: 1, MaxTotalJobs: 1},
	})
	if err == nil || err.Error() != "create job manager: policy for tracing is required" {
		t.Fatalf("normalizeManagerConfig() error = %v", err)
	}
}

func TestManagerCreateTreatsEachRequestAsIndependent(t *testing.T) {
	release := make(chan struct{})
	client := &stubNodeClient{startOperation: func(
		context.Context,
		string,
		*nodeapi.StartOperationRequest,
	) (*nodeapi.Operation, error) {
		<-release
		return nil, errors.New("Node unavailable")
	}}
	manager := testManager(newMemoryStore(), client)

	first, err := manager.Create(t.Context(), testCreateRequest())
	if err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	second, err := manager.Create(t.Context(), testCreateRequest())
	if err != nil {
		t.Fatalf("second Create() error = %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("independent Create() IDs are both %q", first.ID)
	}
	if _, err := manager.Create(t.Context(), testCreateRequest()); !errors.Is(err, ErrQuotaExceeded) {
		t.Fatalf("third Create() error = %v, want ErrQuotaExceeded", err)
	}

	close(release)
	if err := manager.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestManagerListPageUsesLookahead(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	manager := testManager(newMemoryStore(
		testJob("job-1", StatusTerminal, now),
		testJob("job-2", StatusTerminal, now),
	), &stubNodeClient{})

	first, err := manager.ListPage(t.Context(), &Query{Limit: 1})
	if err != nil {
		t.Fatalf("ListPage() first page error = %v", err)
	}
	if len(first.Items) != 1 || !first.HasMore {
		t.Fatalf("ListPage() first page = (%d items, has_more=%t)", len(first.Items), first.HasMore)
	}

	last, err := manager.ListPage(t.Context(), &Query{Limit: 1, Offset: 1})
	if err != nil {
		t.Fatalf("ListPage() last page error = %v", err)
	}
	if len(last.Items) != 1 || last.HasMore {
		t.Fatalf("ListPage() last page = (%d items, has_more=%t)", len(last.Items), last.HasMore)
	}
}

func TestManagerShutdownCancelsBlockedSupervisor(t *testing.T) {
	started := make(chan struct{})
	client := &stubNodeClient{startOperation: func(
		ctx context.Context,
		_ string,
		_ *nodeapi.StartOperationRequest,
	) (*nodeapi.Operation, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	manager := testManager(newMemoryStore(), client)

	if _, err := manager.Create(t.Context(), testCreateRequest()); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("supervisor did not start the Node request")
	}

	shutdownCtx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
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
		if saved.StartAttemptedAt.IsZero() || saved.PendingDeadline.IsZero() {
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

func TestManagerStopBeforeDispatchPersistsUserIntent(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	pendingJob := testJob("job-1", StatusPending, now)
	store := newMemoryStore(pendingJob)
	manager := testManager(store, &stubNodeClient{})
	manager.now = func() time.Time { return now.Add(time.Second) }
	runtime := testManagedJob(pendingJob)
	manager.mu.Lock()
	manager.registerLocked(runtime)
	manager.mu.Unlock()

	stopped, err := manager.Stop(t.Context(), pendingJob.ID)
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if stopped.Status != StatusTerminal || stopped.Terminal == nil || stopped.Terminal.Outcome != OutcomeStopped {
		t.Fatalf("Stop() status = %q, want terminal/stopped", stopped.Status)
	}
	got, err := store.Get(t.Context(), pendingJob.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != StatusTerminal || got.Terminal == nil || got.Terminal.Outcome != OutcomeStopped || got.StopReason != StopReasonUser {
		t.Fatalf("stopped Job = (%q, %q)", got.Status, got.StopReason)
	}
	if got.StopRequestedAt.IsZero() || got.EndedAt.IsZero() {
		t.Fatal("stopped Job did not retain stop and terminal timestamps")
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
	runningJob.StartAttemptedAt = now.Add(-2 * time.Minute)
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
	runningJob.NodeUnavailableSince = now.Add(-time.Second)
	runningJob.NodeUnavailableDeadline = now.Add(time.Minute)
	manager := testManager(newMemoryStore(runningJob), &stubNodeClient{})
	manager.config.StatusPollInterval = 5 * time.Second
	manager.now = func() time.Time { return now }

	if got := manager.nextWake(testManagedJob(runningJob)); got != 5*time.Second {
		t.Fatalf("nextWake() = %s, want 5s", got)
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

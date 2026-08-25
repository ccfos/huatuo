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
	saveHook func(*Job, []Status) error
}

func newMemoryStore(jobs ...*Job) *memoryStore {
	store := &memoryStore{jobs: make(map[string]*Job)}
	for _, jobEntity := range jobs {
		store.jobs[jobEntity.ID] = cloneJob(jobEntity)
	}
	return store
}

func (s *memoryStore) Get(_ context.Context, jobID string) (*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobEntity, ok := s.jobs[jobID]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneJob(jobEntity), nil
}

func (s *memoryStore) Create(_ context.Context, jobEntity *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[jobEntity.ID]; ok {
		return ErrAlreadyExists
	}
	s.jobs[jobEntity.ID] = cloneJob(jobEntity)
	return nil
}

func (s *memoryStore) Save(_ context.Context, jobEntity *Job, expected ...Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.jobs[jobEntity.ID]
	if !ok {
		return ErrNotFound
	}
	if len(expected) != 0 {
		matched := false
		for _, status := range expected {
			matched = matched || current.Status == status
		}
		if !matched {
			return ErrConflict
		}
	}
	if s.saveHook != nil {
		if err := s.saveHook(cloneJob(jobEntity), append([]Status(nil), expected...)); err != nil {
			return err
		}
	}
	s.jobs[jobEntity.ID] = cloneJob(jobEntity)
	s.saves = append(s.saves, cloneJob(jobEntity))
	return nil
}

func (s *memoryStore) List(_ context.Context, query *Query) ([]*Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := make([]*Job, 0, len(s.jobs))
	for _, jobEntity := range s.jobs {
		if query != nil && len(query.Statuses) != 0 {
			matched := false
			for _, status := range query.Statuses {
				matched = matched || jobEntity.Status == status
			}
			if !matched {
				continue
			}
		}
		jobs = append(jobs, cloneJob(jobEntity))
	}
	return jobs, nil
}

func (s *memoryStore) Count(context.Context, *Query) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return int64(len(s.jobs)), nil
}

func (s *memoryStore) DeleteTerminalBefore(context.Context, time.Time, int) (int64, error) {
	return 0, nil
}

func (s *memoryStore) Close(context.Context) error {
	return nil
}

type stubNodeClient struct {
	startProfiling func(context.Context, string, *nodeapi.StartProfilingRequest) (*nodeapi.Operation, error)
	getProfiling   func(context.Context, string, string) (*nodeapi.Operation, error)
	stopProfiling  func(context.Context, string, string) (*nodeapi.Operation, error)
}

func (s *stubNodeClient) StartProfiling(
	ctx context.Context,
	host string,
	request *nodeapi.StartProfilingRequest,
) (*nodeapi.Operation, error) {
	if s.startProfiling != nil {
		return s.startProfiling(ctx, host, request)
	}
	return operation(request.RequestID, nodeapi.OperationStatusPending), nil
}

func (s *stubNodeClient) GetProfiling(
	ctx context.Context,
	host string,
	requestID string,
) (*nodeapi.Operation, error) {
	if s.getProfiling != nil {
		return s.getProfiling(ctx, host, requestID)
	}
	return operation(requestID, nodeapi.OperationStatusPending), nil
}

func (s *stubNodeClient) StopProfiling(
	ctx context.Context,
	host string,
	requestID string,
) (*nodeapi.Operation, error) {
	if s.stopProfiling != nil {
		return s.stopProfiling(ctx, host, requestID)
	}
	return operation(requestID, nodeapi.OperationStatusStopped), nil
}

func (*stubNodeClient) StartTracing(
	context.Context,
	string,
	*nodeapi.StartTracingRequest,
) (*nodeapi.Operation, error) {
	return nil, errors.New("unexpected tracing start")
}

func (*stubNodeClient) GetTracing(
	context.Context,
	string,
	string,
) (*nodeapi.Operation, error) {
	return nil, errors.New("unexpected tracing get")
}

func (*stubNodeClient) StopTracing(
	context.Context,
	string,
	string,
) (*nodeapi.Operation, error) {
	return nil, errors.New("unexpected tracing stop")
}

func testManager(store Store, client NodeClient) *Manager {
	return newManagerWithStore(store, client, ManagerConfig{
		Policies: map[Kind]Policy{
			KindProfiling: {MaxJobsPerHost: 2, MaxTotalJobs: 2},
			KindTracing:   {MaxJobsPerHost: 2, MaxTotalJobs: 2},
		},
		StatusPollInterval:         time.Hour,
		PendingTimeout:             time.Minute,
		CompletionGracePeriod:      time.Minute,
		NodeUnavailableGracePeriod: time.Minute,
		JobRetentionPeriod:         time.Hour,
	})
}

func testJob(id string, status Status, now time.Time) *Job {
	jobEntity := &Job{
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
		jobEntity.EndedAt = now
	}
	return jobEntity
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

func TestNormalizeManagerConfigRequiresBothServicePolicies(t *testing.T) {
	_, err := normalizeManagerConfig(ManagerConfig{Policies: map[Kind]Policy{
		KindProfiling: {MaxJobsPerHost: 1, MaxTotalJobs: 1},
	}})
	if err == nil || err.Error() != "create job manager: policy for tracing is required" {
		t.Fatalf("normalizeManagerConfig() error = %v", err)
	}
}

func TestManagerCreateTreatsEachRequestAsIndependent(t *testing.T) {
	release := make(chan struct{})
	client := &stubNodeClient{startProfiling: func(
		context.Context,
		string,
		*nodeapi.StartProfilingRequest,
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
	if err := manager.ShutdownContext(t.Context()); err != nil {
		t.Fatalf("ShutdownContext() error = %v", err)
	}
}

func TestManagerStartPersistsDispatchMarkerBeforeNodeCall(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	jobEntity := testJob("job-1", StatusPending, now)
	store := newMemoryStore(jobEntity)
	manager := testManager(store, nil)
	manager.now = func() time.Time { return now }
	var markerPersisted atomic.Bool
	store.saveHook = func(saved *Job, expected []Status) error {
		if len(expected) != 1 || expected[0] != StatusPending {
			t.Fatalf("Save() expected statuses = %v", expected)
		}
		if saved.StartAttemptedAt.IsZero() || saved.PendingDeadline.IsZero() {
			t.Fatal("Save() did not contain the dispatch marker")
		}
		markerPersisted.Store(true)
		close(manager.stopCh)
		return nil
	}
	manager.nodeClient = &stubNodeClient{startProfiling: func(
		_ context.Context,
		_ string,
		request *nodeapi.StartProfilingRequest,
	) (*nodeapi.Operation, error) {
		if !markerPersisted.Load() {
			t.Fatal("StartProfiling() ran before the dispatch marker was durable")
		}
		return operation(request.RequestID, nodeapi.OperationStatusPending), nil
	}}

	got, err := manager.start(newManagedJob(jobEntity, false))
	if err != nil {
		t.Fatalf("start() error = %v", err)
	}
	if got.RequestID != jobEntity.ID {
		t.Fatalf("start() request ID = %q, want %q", got.RequestID, jobEntity.ID)
	}
}

func TestManagerStopBeforeDispatchPersistsUserIntent(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	jobEntity := testJob("job-1", StatusPending, now)
	store := newMemoryStore(jobEntity)
	manager := testManager(store, &stubNodeClient{})
	manager.now = func() time.Time { return now.Add(time.Second) }
	runtime := newManagedJob(jobEntity, false)
	manager.mu.Lock()
	manager.registerLocked(runtime)
	manager.mu.Unlock()

	if err := manager.Stop(t.Context(), jobEntity.ID); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	got, err := store.Get(t.Context(), jobEntity.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Status != StatusStopped || got.StopReason != StopReasonUser {
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
			wantStatus: StatusOutcomeUnknown,
		},
		{
			name:              "previously observed operation disappeared",
			operationObserved: true,
			wantStatus:        StatusFailed,
			wantReason:        FailureReasonOperationLost,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
			jobEntity := testJob("job-1", StatusPending, now)
			store := newMemoryStore(jobEntity)
			manager := testManager(store, &stubNodeClient{})
			manager.now = func() time.Time { return now.Add(time.Second) }
			runtime := newManagedJob(jobEntity, false)
			runtime.operationObserved = tt.operationObserved
			nodeErr := &nodeclient.Error{
				StatusCode: 404,
				Code:       nodeapi.ErrorCodeOperationNotFound,
				Message:    "operation not found",
			}

			terminal, err := manager.handleNodeError(runtime, nodeErr, false)
			if err != nil || !terminal {
				t.Fatalf("handleNodeError() = (%t, %v)", terminal, err)
			}
			got := runtimeSnapshot(runtime)
			if got.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", got.Status, tt.wantStatus)
			}
			if tt.wantReason == "" {
				if got.Failure != nil {
					t.Fatalf("failure = %+v, want nil", got.Failure)
				}
			} else if got.Failure == nil || got.Failure.Reason != tt.wantReason {
				t.Fatalf("failure = %+v, want reason %q", got.Failure, tt.wantReason)
			}
		})
	}
}

func TestManagerExecutionTimeoutStopsThenFailsJob(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	jobEntity := testJob("job-1", StatusRunning, now.Add(-2*time.Minute))
	jobEntity.StartAttemptedAt = now.Add(-2 * time.Minute)
	jobEntity.PendingDeadline = now.Add(-90 * time.Second)
	jobEntity.StartedAt = now.Add(-time.Minute)
	jobEntity.ExecutionDeadline = now
	store := newMemoryStore(jobEntity)
	var stopCalls atomic.Int32
	client := &stubNodeClient{stopProfiling: func(
		_ context.Context,
		_ string,
		requestID string,
	) (*nodeapi.Operation, error) {
		stopCalls.Add(1)
		return operation(requestID, nodeapi.OperationStatusStopped), nil
	}}
	manager := testManager(store, client)
	manager.now = func() time.Time { return now }
	runtime := newManagedJob(jobEntity, false)

	terminal, err := manager.reconcileAndStop(
		runtime,
		operation(jobEntity.ID, nodeapi.OperationStatusRunning),
	)
	if err != nil || !terminal {
		t.Fatalf("reconcileAndStop() = (%t, %v)", terminal, err)
	}
	got := runtimeSnapshot(runtime)
	if got.Status != StatusFailed || got.Failure == nil ||
		got.Failure.Reason != FailureReasonExecutionTimedOut {
		t.Fatalf("timed-out Job = (%q, %+v)", got.Status, got.Failure)
	}
	if stopCalls.Load() != 1 {
		t.Fatalf("StopProfiling() calls = %d, want 1", stopCalls.Load())
	}
}

func TestManagerNodeUnavailableDoesNotSpinOnBusinessDeadline(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	jobEntity := testJob("job-1", StatusRunning, now.Add(-time.Minute))
	jobEntity.StartedAt = now.Add(-time.Minute)
	jobEntity.ExecutionDeadline = now.Add(-time.Second)
	jobEntity.NodeUnavailableSince = now.Add(-time.Second)
	jobEntity.NodeUnavailableDeadline = now.Add(time.Minute)
	manager := testManager(newMemoryStore(jobEntity), &stubNodeClient{})
	manager.config.StatusPollInterval = 5 * time.Second
	manager.now = func() time.Time { return now }

	if got := manager.nextWake(newManagedJob(jobEntity, false)); got != 5*time.Second {
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

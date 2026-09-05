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
	"testing"
	"time"

	nodeapi "huatuo-bamai/apis/v1/node"
	"huatuo-bamai/pkg/observation"
	"huatuo-bamai/pkg/profiling"

	"github.com/google/uuid"
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

func (s *memoryStore) Close() error {
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

func TestManagerCreateClassifiesInvalidRequest(t *testing.T) {
	manager := testManager(newMemoryStore(), &stubNodeClient{})

	if _, err := manager.Create(t.Context(), nil); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("Create(nil) error = %v, want ErrInvalidQuery", err)
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
	parsedID, err := uuid.Parse(first.ID)
	if err != nil {
		t.Fatalf("parse first Create() ID %q: %v", first.ID, err)
	}
	if parsedID.String() != first.ID {
		t.Fatalf("first Create() ID = %q, want canonical UUID", first.ID)
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

	if _, err := manager.ListPage(t.Context(), &Query{Limit: 1, Offset: -1}); !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("ListPage() negative offset error = %v, want ErrInvalidQuery", err)
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
	if got.EndedAt.IsZero() {
		t.Fatal("stopped Job did not retain the terminal timestamp")
	}
}

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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
)

type stubJobStore struct {
	mu          sync.Mutex
	saveCalls   []*Job
	deleteCalls []string
	getFunc     func(jobID string) (*Job, error)
	listFunc    func(query *JobQuery) ([]*Job, error)
	saveFunc    func(job *Job) error
	saveErr     error
	deleteErr   error
}

func (s *stubJobStore) Close(context.Context) error { return nil }

func (s *stubJobStore) Count(context.Context, *JobQuery) (int64, error) { return 0, nil }

func (s *stubJobStore) Get(_ context.Context, jobID string) (*Job, error) {
	if s.getFunc != nil {
		return s.getFunc(jobID)
	}
	return nil, nil
}

func (s *stubJobStore) Save(_ context.Context, job *Job) error {
	s.mu.Lock()
	s.saveCalls = append(s.saveCalls, cloneJob(job))
	s.mu.Unlock()
	if s.saveFunc != nil {
		return s.saveFunc(job)
	}
	return s.saveErr
}

func (s *stubJobStore) Create(ctx context.Context, job *Job) error {
	return s.Save(ctx, job)
}

func (s *stubJobStore) Delete(_ context.Context, jobID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deleteCalls = append(s.deleteCalls, jobID)
	return s.deleteErr
}

func (s *stubJobStore) savedJobs() []*Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Job(nil), s.saveCalls...)
}

func (s *stubJobStore) deletedJobIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.deleteCalls...)
}

func (s *stubJobStore) List(_ context.Context, query *JobQuery) ([]*Job, error) {
	if s.listFunc != nil {
		return s.listFunc(query)
	}
	return nil, nil
}

type stubNodeAgent struct {
	startTaskCalls    atomic.Int32
	stopTaskCalls     atomic.Int32
	startTaskFunc     func(host, container string, args *AgentTaskRequest) (string, error)
	stopTaskFunc      func(host, taskID string, force bool) error
	getTaskStatusFunc func(host, taskID string) (string, *Result, error)
}

func (s *stubNodeAgent) StartTask(host, container string, args *AgentTaskRequest) (string, error) {
	s.startTaskCalls.Add(1)
	if s.startTaskFunc != nil {
		return s.startTaskFunc(host, container, args)
	}
	return "", nil
}

func (s *stubNodeAgent) StopTask(host, taskID string, force bool) error {
	s.stopTaskCalls.Add(1)
	if s.stopTaskFunc != nil {
		return s.stopTaskFunc(host, taskID, force)
	}
	return nil
}

func (s *stubNodeAgent) GetTaskStatus(host, taskID string) (string, *Result, error) {
	if s.getTaskStatusFunc != nil {
		return s.getTaskStatusFunc(host, taskID)
	}
	return "", nil, nil
}

func (s *stubNodeAgent) StartTaskContext(_ context.Context, host, container string, args *AgentTaskRequest) (string, error) {
	return s.StartTask(host, container, args)
}

func (s *stubNodeAgent) StopTaskContext(_ context.Context, host, taskID string, force bool) error {
	return s.StopTask(host, taskID, force)
}

func (s *stubNodeAgent) GetTaskStatusContext(_ context.Context, host, taskID string) (string, *Result, error) {
	return s.GetTaskStatus(host, taskID)
}

func newTestManager(storage Store, nodeAgent NodeAgent) *Manager {
	return newManagerWithStore(storage, nodeAgent, ManagerConfig{
		TypePolicies: map[JobType]TypePolicy{
			"oncpu": {MaxJobsPerHost: 2, MaxTotalJobs: 3},
		},
	})
}

func newRunningJob(jobID string) *Job {
	return &Job{
		Type:        "oncpu",
		ID:          jobID,
		Username:    "operator-2026",
		UserID:      "operator-2026",
		ContainerID: "payment-worker",
		Hostname:    "huatuo-dev",
		AgentTaskID: "agent-task-2026",
		Status:      JobStatusRunning,
		AgentTask: AgentTaskRequest{
			TracerName:   "oncpu",
			TraceTimeout: 60,
			DataType:     "flamegraph",
		},
		stopCh: make(chan struct{}),
	}
}

func TestManagerCreateKeepsPendingJobWhenDispatchIsUncertain(t *testing.T) {
	store := &stubJobStore{}
	manager := newTestManager(store, &stubNodeAgent{
		startTaskFunc: func(_, _ string, _ *AgentTaskRequest) (string, error) {
			return "", fmt.Errorf("%w: connection reset", ErrAgentDispatchUncertain)
		},
	})

	created, err := manager.CreateContext(t.Context(), &CreateJobRequest{
		Type:     "oncpu",
		Hostname: "node-a",
		AgentTask: &AgentTaskRequest{
			TracerName:   "oncpu",
			TraceTimeout: 60,
		},
	})
	if err != nil {
		t.Fatalf("CreateContext() error = %v", err)
	}
	if created.Status != JobStatusPending {
		t.Fatalf("created status = %q, want %q", created.Status, JobStatusPending)
	}
	if !strings.HasPrefix(created.ID, "id-") || len(created.ID) != len("id-")+36 {
		t.Fatalf("created ID = %q, want full UUID", created.ID)
	}

	if err := manager.ShutdownContext(t.Context()); err != nil {
		t.Fatalf("ShutdownContext() error = %v", err)
	}
}

func TestManagerTypePoliciesShareQuotaWithinGroup(t *testing.T) {
	manager := newManagerWithStore(&stubJobStore{}, &stubNodeAgent{
		startTaskFunc: func(_, _ string, _ *AgentTaskRequest) (string, error) {
			return "agent-task-2026", nil
		},
	}, ManagerConfig{TypePolicies: map[JobType]TypePolicy{
		"profiling_cpu": {
			Group:          "profiling",
			MaxJobsPerHost: 1,
			MaxTotalJobs:   2,
		},
		"profiling_memory": {
			Group:          "profiling",
			MaxJobsPerHost: 1,
			MaxTotalJobs:   2,
		},
		"tracing": {
			Group:          "tracing",
			MaxJobsPerHost: 1,
			MaxTotalJobs:   2,
		},
	}})

	cpuJob, err := manager.CreateContext(t.Context(), &CreateJobRequest{
		UserID:   "operator-2026",
		Hostname: "huatuo-dev",
		Type:     "profiling_cpu",
		AgentTask: &AgentTaskRequest{
			TracerName:   "profiler",
			TraceTimeout: 60,
			DataType:     "db-json",
		},
	})
	if err != nil {
		t.Fatalf("create CPU profiling job: %v", err)
	}
	defer func() { _ = manager.ShutdownContext(t.Context()) }()

	_, err = manager.CreateContext(t.Context(), &CreateJobRequest{
		UserID:   "operator-2026",
		Hostname: "huatuo-dev",
		Type:     "profiling_memory",
		AgentTask: &AgentTaskRequest{
			TracerName:   "profiler",
			TraceTimeout: 60,
			DataType:     "db-json",
		},
	})
	if err == nil || !strings.Contains(err.Error(), "maximum number of profiling jobs") {
		t.Fatalf("create memory profiling job error = %v, want shared quota error", err)
	}

	traceJob, err := manager.CreateContext(t.Context(), &CreateJobRequest{
		UserID:   "operator-2026",
		Hostname: "huatuo-dev",
		Type:     "tracing",
		AgentTask: &AgentTaskRequest{
			TracerName:   "tracer",
			TraceTimeout: 60,
			DataType:     "db",
		},
	})
	if err != nil {
		t.Fatalf("create tracing job with independent quota: %v", err)
	}
	if err := manager.StopContext(t.Context(), cpuJob.ID, true); err != nil {
		t.Fatalf("stop CPU profiling job: %v", err)
	}
	if err := manager.StopContext(t.Context(), traceJob.ID, true); err != nil {
		t.Fatalf("stop tracing job: %v", err)
	}
}

func TestValidateManagerConfigRejectsInconsistentGroupQuota(t *testing.T) {
	err := validateManagerConfig(ManagerConfig{TypePolicies: map[JobType]TypePolicy{
		"profiling_cpu": {
			Group:          "profiling",
			MaxJobsPerHost: 1,
			MaxTotalJobs:   10,
		},
		"profiling_memory": {
			Group:          "profiling",
			MaxJobsPerHost: 2,
			MaxTotalJobs:   10,
		},
	}})
	if err == nil || !strings.Contains(err.Error(), "inconsistent limits") {
		t.Fatalf("validateManagerConfig() error = %v, want inconsistent limits", err)
	}
}

func TestManagerGetByTypesHidesOtherJobTypes(t *testing.T) {
	manager := newTestManager(&stubJobStore{getFunc: func(string) (*Job, error) {
		return &Job{ID: "job-2026", Type: "profiling_cpu"}, nil
	}}, &stubNodeAgent{})

	_, err := manager.GetByTypesContext(t.Context(), "job-2026", JobTypeTracing)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetByTypes() error = %v, want ErrNotFound", err)
	}
}

func TestManagerPersistsPendingBeforeAgentDispatch(t *testing.T) {
	storage := &stubJobStore{}
	agent := &stubNodeAgent{
		startTaskFunc: func(_, _ string, args *AgentTaskRequest) (string, error) {
			saved := storage.savedJobs()
			if len(saved) != 1 || saved[0].Status != JobStatusPending {
				t.Fatalf("jobs saved before dispatch=%+v, want one pending job", saved)
			}
			if args.RequestID == "" || args.RequestID != saved[0].ID {
				t.Fatalf("agent request ID=%q, want job ID %q", args.RequestID, saved[0].ID)
			}
			return args.RequestID, nil
		},
	}
	manager := newTestManager(storage, agent)
	request := &CreateJobRequest{
		Hostname: "huatuo-dev",
		Type:     "oncpu",
		AgentTask: &AgentTaskRequest{
			TracerName:   "profiler",
			TraceTimeout: 60,
		},
	}

	created, err := manager.CreateContext(t.Context(), request)
	if err != nil {
		t.Fatalf("CreateContext() error=%v", err)
	}
	if request.AgentTask.RequestID != "" {
		t.Fatalf("CreateContext() mutated request ID=%q", request.AgentTask.RequestID)
	}
	saved := storage.savedJobs()
	if len(saved) != 2 || saved[1].Status != JobStatusRunning {
		t.Fatalf("saved jobs=%+v, want pending then running", saved)
	}
	if err := manager.StopContext(t.Context(), created.ID, true); err != nil {
		t.Fatalf("StopContext() error=%v", err)
	}
}

func TestManagerRetainsQuotaWhenTerminalPersistenceFails(t *testing.T) {
	storage := &stubJobStore{saveErr: errors.New("disk full")}
	manager := newTestManager(storage, &stubNodeAgent{})
	active := newRunningJob("job-persist-2026")
	manager.jobs[active.ID] = active
	manager.jobsByHost[quotaHostKey(active.Hostname, "oncpu")] = 1

	err := manager.finishJob(t.Context(), active, JobStatusCompleted, "", &Result{})
	if !errors.Is(err, ErrPersistence) {
		t.Fatalf("finishJob() error=%v, want ErrPersistence", err)
	}
	if !manager.jobIsActive(active.ID) {
		t.Fatal("finishJob() removed active job after persistence failure")
	}
	if got := manager.jobsByHost[quotaHostKey(active.Hostname, "oncpu")]; got != 1 {
		t.Fatalf("host quota=%d, want 1", got)
	}
}

func TestManagerRestartsRecoveredPendingJob(t *testing.T) {
	storage := &stubJobStore{}
	agent := &stubNodeAgent{
		getTaskStatusFunc: func(_, _ string) (string, *Result, error) {
			return AgentStatusNotExist, nil, nil
		},
		startTaskFunc: func(_, _ string, args *AgentTaskRequest) (string, error) {
			if args.RequestID != "job-pending-2026" {
				t.Fatalf("restart request ID=%q, want job-pending-2026", args.RequestID)
			}
			return args.RequestID, nil
		},
	}
	manager := newTestManager(storage, agent)
	pending := newRunningJob("job-pending-2026")
	pending.Status = JobStatusPending
	pending.AgentTaskID = pending.ID
	manager.jobs[pending.ID] = pending
	manager.jobsByHost[quotaHostKey(pending.Hostname, "oncpu")] = 1

	status, err := manager.checkAndUpdateJobStatus(t.Context(), pending)
	if err != nil {
		t.Fatalf("checkAndUpdateJobStatus() error=%v", err)
	}
	if status != AgentStatusRunning || pending.Status != JobStatusRunning {
		t.Fatalf("statuses=(%q,%q), want running", status, pending.Status)
	}
	if got := agent.startTaskCalls.Load(); got != 1 {
		t.Fatalf("StartTask() calls=%d, want 1", got)
	}
}

func TestManagerRecoverJobsRestoresActiveQuota(t *testing.T) {
	recovered := newRunningJob("job-recovered-2026")
	storage := &stubJobStore{listFunc: func(query *JobQuery) ([]*Job, error) {
		wantStatuses := []JobStatus{JobStatusPending, JobStatusRunning}
		if diff := cmp.Diff(wantStatuses, query.Statuses); diff != "" {
			t.Fatalf("recovery statuses mismatch (-want +got):\n%s", diff)
		}
		return []*Job{recovered}, nil
	}}
	manager := newTestManager(storage, &stubNodeAgent{})
	if err := manager.recoverJobs(t.Context()); err != nil {
		t.Fatalf("recoverJobs() error=%v", err)
	}
	if !manager.jobIsActive(recovered.ID) {
		t.Fatal("recovered job is not active")
	}
	if got := manager.jobsByHost[quotaHostKey(recovered.Hostname, "oncpu")]; got != 1 {
		t.Fatalf("recovered host quota=%d, want 1", got)
	}
	if err := manager.ShutdownContext(t.Context()); err != nil {
		t.Fatalf("ShutdownContext() error=%v", err)
	}
}

// TestManagerCreate tests key branches of Manager.Create, including missing timeout/duration in request, per-host job limit reached, total job limit reached, and successful job creation with field population, task dispatch, and memory index updates.
func TestManagerCreate(t *testing.T) {
	t.Run("timeout or duration required", func(t *testing.T) {
		storage := &stubJobStore{}
		nodeAgent := &stubNodeAgent{}
		manager := newTestManager(storage, nodeAgent)

		job, err := manager.CreateContext(t.Context(), &CreateJobRequest{
			UserID:      "operator-2026",
			ContainerID: "payment-worker",
			Hostname:    "huatuo-dev",
			Type:        "oncpu",
			AgentTask: &AgentTaskRequest{
				TracerName: "oncpu",
				DataType:   "flamegraph",
			},
		})
		if err == nil {
			t.Errorf("Create() error=nil, want validation error")
		}
		if job != nil {
			t.Errorf("Create() job=%+v, want nil", job)
		}
		if got := nodeAgent.startTaskCalls.Load(); got != 0 {
			t.Errorf("StartTask() call count=%d, want 0", got)
		}
	})

	t.Run("per host limit reached", func(t *testing.T) {
		storage := &stubJobStore{}
		nodeAgent := &stubNodeAgent{}
		manager := newTestManager(storage, nodeAgent)
		manager.jobsByHost[quotaHostKey("huatuo-dev", "oncpu")] = 2

		job, err := manager.CreateContext(t.Context(), &CreateJobRequest{
			UserID:      "operator-2026",
			ContainerID: "payment-worker",
			Hostname:    "huatuo-dev",
			Type:        "oncpu",
			AgentTask: &AgentTaskRequest{
				TracerName:   "oncpu",
				TraceTimeout: 60,
				DataType:     "flamegraph",
			},
		})

		if err == nil || !strings.Contains(err.Error(), "maximum number of oncpu jobs reached for host") {
			t.Errorf("Create() error=%v, want host limit error", err)
		}
		if job != nil {
			t.Errorf("Create() job=%+v, want nil", job)
		}
	})

	t.Run("total limit reached", func(t *testing.T) {
		storage := &stubJobStore{}
		nodeAgent := &stubNodeAgent{}
		manager := newManagerWithStore(storage, nodeAgent, ManagerConfig{
			TypePolicies: map[JobType]TypePolicy{
				"oncpu": {MaxJobsPerHost: 3, MaxTotalJobs: 2},
			},
		})
		manager.jobs["job-20260101"] = newRunningJob("job-20260101")
		manager.jobs["job-20260102"] = newRunningJob("job-20260102")

		job, err := manager.CreateContext(t.Context(), &CreateJobRequest{
			UserID:      "operator-2026",
			ContainerID: "payment-worker",
			Hostname:    "huatuo-dev",
			Type:        "oncpu",
			AgentTask: &AgentTaskRequest{
				TracerName:   "oncpu",
				TraceTimeout: 60,
				DataType:     "flamegraph",
			},
		})

		if err == nil || !strings.Contains(err.Error(), "maximum number of total oncpu jobs reached") {
			t.Errorf("Create() error=%v, want total limit error", err)
		}
		if job != nil {
			t.Errorf("Create() job=%+v, want nil", job)
		}
	})

	t.Run("create success", func(t *testing.T) {
		storage := &stubJobStore{}
		nodeAgent := &stubNodeAgent{
			startTaskFunc: func(host, container string, args *AgentTaskRequest) (string, error) {
				return "agent-task-2026", nil
			},
		}
		manager := newTestManager(storage, nodeAgent)
		privateData := json.RawMessage(`{"language":"go"}`)

		job, err := manager.CreateContext(t.Context(), &CreateJobRequest{
			UserID:      "operator-2026",
			ContainerID: "payment-worker",
			Hostname:    "huatuo-dev",
			Type:        "oncpu",
			PrivateData: privateData,
			AgentTask: &AgentTaskRequest{
				TracerName:   "oncpu",
				TraceTimeout: 60,
				DataType:     "flamegraph",
				TracerArgs:   []string{"--pid=9527"},
			},
		})
		if err != nil {
			t.Errorf("Create() error=%v, want nil", err)
			return
		}
		if job == nil {
			t.Errorf("Create() job=nil, want non-nil")
			return
		}
		if job.Status != JobStatusRunning {
			t.Errorf("Create() status=%s, want %s", job.Status, JobStatusRunning)
		}
		if job.AgentTaskID != "agent-task-2026" {
			t.Errorf("Create() agent task id=%q, want %q", job.AgentTaskID, "agent-task-2026")
		}
		if !strings.HasPrefix(job.ID, "id-") {
			t.Errorf("Create() job id=%q, want prefix %q", job.ID, "id-")
		}
		if job.Username != "operator-2026" {
			t.Errorf("Create() username=%q, want %q", job.Username, "operator-2026")
		}
		if got := nodeAgent.startTaskCalls.Load(); got != 1 {
			t.Errorf("StartTask() call count=%d, want 1", got)
		}
		privateData[0] = '['
		var gotPrivateData map[string]string
		if err := json.Unmarshal(job.PrivateData, &gotPrivateData); err != nil {
			t.Fatalf("unmarshal job private data: %v", err)
		}
		if gotPrivateData["language"] != "go" {
			t.Errorf("Create() private data=%s, want language go", job.PrivateData)
		}

		storedJobVal, exists := manager.jobs[job.ID]
		if !exists {
			t.Errorf("jobs.Load(%q) exists=false, want true", job.ID)
		} else if storedJobVal.ID != job.ID {
			t.Errorf("jobs.Load(%q) returned unexpected job", job.ID)
		}

		jobCountVal, exists := manager.jobsByHost[quotaHostKey("huatuo-dev", "oncpu")]
		if !exists {
			t.Errorf("jobsByHost.Load(%q) exists=false, want true", "huatuo-dev")
		} else if jobCountVal != 1 {
			t.Errorf("jobsByHost.Load(%q)=%d, want 1", "huatuo-dev", jobCountVal)
		}

		if stopErr := manager.StopContext(t.Context(), job.ID, true); stopErr != nil {
			t.Errorf("Stop(%q) error=%v, want nil", job.ID, stopErr)
		}
	})
}

// TestManagerStop tests Manager.Stop behavior, including returning nil when job doesn't exist, and stopping a running job by dispatching stop command, closing stop channel, updating status to stopped, and removing from memory index.
func TestManagerStop(t *testing.T) {
	t.Run("job not found returns nil", func(t *testing.T) {
		storage := &stubJobStore{}
		nodeAgent := &stubNodeAgent{}
		manager := newTestManager(storage, nodeAgent)

		err := manager.StopContext(t.Context(), "job-not-found", true)
		if err != nil {
			t.Errorf("Stop() error=%v, want nil", err)
		}
		if got := nodeAgent.stopTaskCalls.Load(); got != 0 {
			t.Errorf("StopTask() call count=%d, want 0", got)
		}
	})

	t.Run("running job is stopped", func(t *testing.T) {
		storage := &stubJobStore{}
		nodeAgent := &stubNodeAgent{}
		manager := newTestManager(storage, nodeAgent)
		job := newRunningJob("job-running-2026")
		manager.jobs[job.ID] = job
		manager.jobsByHost[quotaHostKey(job.Hostname, "oncpu")] = 1

		err := manager.StopContext(t.Context(), job.ID, true)
		if err != nil {
			t.Errorf("Stop() error=%v, want nil", err)
		}
		if got := nodeAgent.stopTaskCalls.Load(); got != 1 {
			t.Errorf("StopTask() call count=%d, want 1", got)
		}
		if job.Status != JobStatusStopped {
			t.Errorf("Stop() status=%s, want %s", job.Status, JobStatusStopped)
		}
		if job.ErrorMessage != "job stopped by user" {
			t.Errorf("Stop() error message=%q, want %q", job.ErrorMessage, "job stopped by user")
		}
		if got := len(storage.savedJobs()); got != 1 {
			t.Errorf("storage.Save() call count=%d, want 1", got)
		}

		select {
		case <-job.stopCh:
		default:
			t.Errorf("job.stopCh was not closed")
		}

		if _, exists := manager.jobs[job.ID]; exists {
			t.Errorf("jobs.Load(%q) exists=true, want false", job.ID)
		}
		if _, exists := manager.jobsByHost[quotaHostKey(job.Hostname, "oncpu")]; exists {
			t.Errorf("jobsByHost.Load(%q) exists=true, want false", job.Hostname)
		}
	})
}

// TestManagerGet covers the Manager.Get read path: verifies in-memory jobs are returned first and storage is consulted as a fallback when the job is not in memory.
func TestManagerGet(t *testing.T) {
	t.Run("returns in-memory job first", func(t *testing.T) {
		storage := &stubJobStore{
			getFunc: func(jobID string) (*Job, error) {
				t.Errorf("storage Get() should not be called for in-memory job %q", jobID)
				return nil, nil
			},
		}
		manager := newTestManager(storage, &stubNodeAgent{})
		runningJob := newRunningJob("job-memory-2026")
		manager.jobs[runningJob.ID] = runningJob

		gotJob, err := manager.GetContext(t.Context(), "job-memory-2026")
		if err != nil {
			t.Errorf("Get() error=%v, want nil", err)
		}
		if gotJob.ID != runningJob.ID {
			t.Errorf("Get() job ID=%q, want %q", gotJob.ID, runningJob.ID)
		}
	})

	t.Run("falls back to store", func(t *testing.T) {
		storage := &stubJobStore{
			getFunc: func(jobID string) (*Job, error) {
				if jobID != "job-archived-2026" {
					t.Errorf("Get() jobID=%q, want %q", jobID, "job-archived-2026")
				}
				return &Job{
					ID:     "job-archived-2026",
					Status: JobStatusCompleted,
				}, nil
			},
		}
		manager := newTestManager(storage, &stubNodeAgent{})

		gotJob, err := manager.GetContext(t.Context(), "job-archived-2026")
		if err != nil {
			t.Errorf("Get() error=%v, want nil", err)
		}
		if gotJob == nil {
			t.Errorf("Get() returned nil job")
			return
		}
		if gotJob.ID != "job-archived-2026" {
			t.Errorf("Get() job id=%q, want %q", gotJob.ID, "job-archived-2026")
		}
	})
}

// TestManagerList tests storage-backed query assembly and filtering behavior.
func TestManagerList(t *testing.T) {
	cases := []struct {
		name      string
		userID    string
		isAdmin   bool
		filter    *JobQuery
		wantIDs   []string
		wantQuery JobQuery
	}{
		{
			name:    "non admin with filter",
			userID:  "operator-2026",
			isAdmin: false,
			filter: &JobQuery{
				ContainerID: "payment-worker",
				Hostname:    "huatuo-dev",
				Status:      string(JobStatusRunning),
				Types:       []JobType{"oncpu"},
			},
			wantIDs: []string{"job-archived-2026"},
			wantQuery: JobQuery{
				UserID:      "operator-2026",
				IsAdmin:     false,
				ContainerID: "payment-worker",
				Hostname:    "huatuo-dev",
				Status:      string(JobStatusRunning),
				Types:       []JobType{"oncpu"},
			},
		},
		{
			name:    "admin without filter",
			userID:  "operator-2026",
			isAdmin: true,
			wantIDs: []string{"job-archived-2026"},
			wantQuery: JobQuery{
				UserID:  "operator-2026",
				IsAdmin: true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storage := &stubJobStore{
				listFunc: func(query *JobQuery) ([]*Job, error) {
					if diff := cmp.Diff(tc.wantQuery, *query); diff != "" {
						t.Errorf("List() query mismatch (-want +got):\n%s", diff)
					}

					return []*Job{
						{
							ID:          "job-archived-2026",
							UserID:      "operator-2026",
							ContainerID: "payment-worker",
							Hostname:    "huatuo-dev",
							Status:      JobStatusRunning,
							Type:        "oncpu",
						},
					}, nil
				},
			}
			manager := newTestManager(storage, &stubNodeAgent{})

			jobs, err := manager.ListContext(t.Context(), tc.userID, tc.isAdmin, tc.filter)
			if err != nil {
				t.Errorf("List() error=%v, want nil", err)
				return
			}
			if len(jobs) != len(tc.wantIDs) {
				t.Errorf("List() len=%d, want %d", len(jobs), len(tc.wantIDs))
			}

			gotIDs := make(map[string]bool, len(jobs))
			for _, job := range jobs {
				gotIDs[job.ID] = true
			}
			for _, wantID := range tc.wantIDs {
				if !gotIDs[wantID] {
					t.Errorf("List() expected job id %q was not returned", wantID)
				}
			}
		})
	}
}

// TestManagerCheckAndUpdateJobStatus tests Manager.checkAndUpdateJobStatus status mapping, including when agent returns completed, failed, not_exist, running, or query error, verifying local job status, results, storage operations, and memory index changes.
func TestManagerCheckAndUpdateJobStatus(t *testing.T) {
	cases := []struct {
		name     string
		status   string
		result   *Result
		err      error
		validate func(t *testing.T, manager *Manager, job *Job, storage *stubJobStore, gotStatus string, gotErr error)
	}{
		{
			name:   "completed",
			status: AgentStatusCompleted,
			result: &Result{URL: "s3://huatuo-region/job-report-2026", Error: ""},
			validate: func(t *testing.T, manager *Manager, job *Job, storage *stubJobStore, gotStatus string, gotErr error) {
				if gotErr != nil {
					t.Errorf("checkAndUpdateJobStatus() error=%v, want nil", gotErr)
				}
				if gotStatus != AgentStatusCompleted {
					t.Errorf("checkAndUpdateJobStatus() status=%q, want %q", gotStatus, AgentStatusCompleted)
				}
				if job.Status != JobStatusCompleted {
					t.Errorf("job.Status=%s, want %s", job.Status, JobStatusCompleted)
				}
				if job.Result.URL != "s3://huatuo-region/job-report-2026" {
					t.Errorf("job.Result.URL=%q, want %q", job.Result.URL, "s3://huatuo-region/job-report-2026")
				}
				if got := len(storage.savedJobs()); got != 1 {
					t.Errorf("storage.Save() call count=%d, want 1", got)
				}
				if _, exists := manager.jobs[job.ID]; exists {
					t.Errorf("jobs.Load(%q) exists=true, want false", job.ID)
				}
			},
		},
		{
			name:   "failed",
			status: AgentStatusFailed,
			result: &Result{Error: "trace process exited with code 2"},
			validate: func(t *testing.T, manager *Manager, job *Job, storage *stubJobStore, gotStatus string, gotErr error) {
				if gotErr != nil {
					t.Errorf("checkAndUpdateJobStatus() error=%v, want nil", gotErr)
				}
				if gotStatus != AgentStatusFailed {
					t.Errorf("checkAndUpdateJobStatus() status=%q, want %q", gotStatus, AgentStatusFailed)
				}
				if job.Status != JobStatusFailed {
					t.Errorf("job.Status=%s, want %s", job.Status, JobStatusFailed)
				}
				if job.ErrorMessage != "job failed: trace process exited with code 2" {
					t.Errorf("job.ErrorMessage=%q, want %q", job.ErrorMessage, "job failed: trace process exited with code 2")
				}
				if got := len(storage.savedJobs()); got != 1 {
					t.Errorf("storage.Save() call count=%d, want 1", got)
				}
				if _, exists := manager.jobsByHost[quotaHostKey(job.Hostname, "oncpu")]; exists {
					t.Errorf("jobsByHost.Load(%q) exists=true, want false", job.Hostname)
				}
			},
		},
		{
			name:   "not exist on agent",
			status: AgentStatusNotExist,
			validate: func(t *testing.T, manager *Manager, job *Job, storage *stubJobStore, gotStatus string, gotErr error) {
				if gotErr != nil {
					t.Errorf("checkAndUpdateJobStatus() error=%v, want nil", gotErr)
				}
				if gotStatus != AgentStatusNotExist {
					t.Errorf("checkAndUpdateJobStatus() status=%q, want %q", gotStatus, AgentStatusNotExist)
				}
				if job.Status != JobStatusFailed {
					t.Errorf("job.Status=%s, want %s", job.Status, JobStatusFailed)
				}
				if job.ErrorMessage != "job does not exist on agent" {
					t.Errorf("job.ErrorMessage=%q, want %q", job.ErrorMessage, "job does not exist on agent")
				}
				if got := len(storage.savedJobs()); got != 1 {
					t.Errorf("storage.Save() call count=%d, want 1", got)
				}
			},
		},
		{
			name:   "running",
			status: AgentStatusRunning,
			validate: func(t *testing.T, manager *Manager, job *Job, storage *stubJobStore, gotStatus string, gotErr error) {
				if gotErr != nil {
					t.Errorf("checkAndUpdateJobStatus() error=%v, want nil", gotErr)
				}
				if gotStatus != AgentStatusRunning {
					t.Errorf("checkAndUpdateJobStatus() status=%q, want %q", gotStatus, AgentStatusRunning)
				}
				if job.Status != JobStatusRunning {
					t.Errorf("job.Status=%s, want %s", job.Status, JobStatusRunning)
				}
				if got := len(storage.savedJobs()); got != 0 {
					t.Errorf("storage.Save() call count=%d, want 0", got)
				}
				if _, exists := manager.jobs[job.ID]; !exists {
					t.Errorf("jobs.Load(%q) exists=false, want true", job.ID)
				}
			},
		},
		{
			name: "agent error",
			err:  errors.New("agent timeout"),
			validate: func(t *testing.T, manager *Manager, job *Job, storage *stubJobStore, gotStatus string, gotErr error) {
				if gotErr == nil || gotErr.Error() != "agent timeout" {
					t.Errorf("checkAndUpdateJobStatus() error=%v, want %q", gotErr, "agent timeout")
				}
				if gotStatus != "" {
					t.Errorf("checkAndUpdateJobStatus() status=%q, want empty", gotStatus)
				}
				if job.Status != JobStatusRunning {
					t.Errorf("job.Status=%s, want %s", job.Status, JobStatusRunning)
				}
				if got := len(storage.savedJobs()); got != 0 {
					t.Errorf("storage.Save() call count=%d, want 0", got)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storage := &stubJobStore{}
			nodeAgent := &stubNodeAgent{
				getTaskStatusFunc: func(host, taskID string) (string, *Result, error) {
					if host != "huatuo-dev" {
						t.Errorf("GetTaskStatus() host=%q, want %q", host, "huatuo-dev")
					}
					if taskID != "agent-task-2026" {
						t.Errorf("GetTaskStatus() taskID=%q, want %q", taskID, "agent-task-2026")
					}
					return tc.status, tc.result, tc.err
				},
			}
			manager := newTestManager(storage, nodeAgent)
			job := newRunningJob("job-status-2026")
			manager.jobs[job.ID] = job
			manager.jobsByHost[quotaHostKey(job.Hostname, "oncpu")] = 1

			gotStatus, gotErr := manager.checkAndUpdateJobStatus(t.Context(), job)
			tc.validate(t, manager, job, storage, gotStatus, gotErr)
		})
	}
}

// TestMonitorJobDeferNoNilPanic verifies that monitorJob's defer does not panic
// when the manager is shut down while a job is still running. Before the fix,
// the defer called err.Error() when err was nil, causing a nil pointer dereference.
func TestMonitorJobDeferNoNilPanic(t *testing.T) {
	storage := &stubJobStore{}
	nodeAgent := &stubNodeAgent{
		startTaskFunc: func(host, container string, args *AgentTaskRequest) (string, error) {
			return "agent-task-2026", nil
		},
		stopTaskFunc: func(host, taskID string, force bool) error {
			return nil
		},
		getTaskStatusFunc: func(host, taskID string) (string, *Result, error) {
			return AgentStatusRunning, &Result{}, nil
		},
	}
	manager := newTestManager(storage, nodeAgent)

	_, err := manager.CreateContext(t.Context(), &CreateJobRequest{
		UserID:      "operator-2026",
		ContainerID: "payment-worker",
		Hostname:    "huatuo-dev",
		Type:        "oncpu",
		AgentTask: &AgentTaskRequest{
			TracerName:   "oncpu",
			TraceTimeout: 300,
			DataType:     "flamegraph",
		},
	})
	if err != nil {
		t.Fatalf("Create() error=%v, want nil", err)
	}

	// Shutdown owns active jobs and stops them before releasing storage.
	_ = manager.ShutdownContext(t.Context())

	savedJobs := storage.savedJobs()
	if len(savedJobs) == 0 {
		t.Fatal("storage.Save() call count=0, want at least 1")
	}
	lastSave := savedJobs[len(savedJobs)-1]
	if lastSave.Status != JobStatusStopped {
		t.Errorf("job.Status=%s, want %s", lastSave.Status, JobStatusStopped)
	}
	if lastSave.ErrorMessage == "" {
		t.Errorf("job.ErrorMessage is empty, want non-empty error message")
	}
}

// TestManagerDelete tests Manager.Delete deletion restrictions, including preventing deletion of running jobs in memory, preventing deletion of running jobs in storage, and calling storage layer to delete completed jobs.
func TestManagerDelete(t *testing.T) {
	t.Run("running job in memory cannot be deleted", func(t *testing.T) {
		storage := &stubJobStore{}
		manager := newTestManager(storage, &stubNodeAgent{})
		manager.jobs["job-running-2026"] = newRunningJob("job-running-2026")

		err := manager.DeleteContext(t.Context(), "job-running-2026")
		if !errors.Is(err, ErrCannotDeleteRunning) {
			t.Errorf("Delete() error=%v, want %v", err, ErrCannotDeleteRunning)
		}
		if got := len(storage.deletedJobIDs()); got != 0 {
			t.Errorf("storage.Delete() call count=%d, want 0", got)
		}
	})

	t.Run("running job in storage cannot be deleted", func(t *testing.T) {
		storage := &stubJobStore{
			getFunc: func(jobID string) (*Job, error) {
				if jobID != "job-running-2026" {
					t.Errorf("Get() jobID=%q, want %q", jobID, "job-running-2026")
				}
				return &Job{
					ID:     "job-running-2026",
					Status: JobStatusRunning,
				}, nil
			},
		}
		manager := newTestManager(storage, &stubNodeAgent{})

		err := manager.DeleteContext(t.Context(), "job-running-2026")
		if !errors.Is(err, ErrCannotDeleteRunning) {
			t.Errorf("Delete() error=%v, want %v", err, ErrCannotDeleteRunning)
		}
		if got := len(storage.deletedJobIDs()); got != 0 {
			t.Errorf("storage.Delete() call count=%d, want 0", got)
		}
	})

	t.Run("completed job in storage is deleted", func(t *testing.T) {
		storage := &stubJobStore{
			getFunc: func(jobID string) (*Job, error) {
				if jobID != "job-completed-2026" {
					t.Errorf("Get() jobID=%q, want %q", jobID, "job-completed-2026")
				}
				return &Job{
					ID:     "job-completed-2026",
					Status: JobStatusCompleted,
				}, nil
			},
		}
		manager := newTestManager(storage, &stubNodeAgent{})

		err := manager.DeleteContext(t.Context(), "job-completed-2026")
		if err != nil {
			t.Errorf("Delete() error=%v, want nil", err)
		}
		deletedJobIDs := storage.deletedJobIDs()
		if len(deletedJobIDs) != 1 {
			t.Errorf("storage.Delete() call count=%d, want 1", len(deletedJobIDs))
		} else if deletedJobIDs[0] != "job-completed-2026" {
			t.Errorf("storage.Delete() condition=%v, want %q", deletedJobIDs[0], "job-completed-2026")
		}
	})
}

func TestManagerCreateRejectsNilArgs(t *testing.T) {
	manager := newTestManager(&stubJobStore{}, &stubNodeAgent{})

	job, err := manager.CreateContext(t.Context(), nil)
	if err == nil || err.Error() != "job request is required" {
		t.Errorf("Create(nil) error=%v, want missing request error", err)
	}
	if job != nil {
		t.Errorf("Create(nil) job=%+v, want nil", job)
	}

	job, err = manager.CreateContext(t.Context(), &CreateJobRequest{})
	if err == nil || err.Error() != "job arguments are required" {
		t.Errorf("Create() error=%v, want missing arguments error", err)
	}
	if job != nil {
		t.Errorf("Create() job=%+v, want nil", job)
	}
}

func TestManagerCreateReservesQuotaAtomically(t *testing.T) {
	const maxJobs = 3
	manager := newManagerWithStore(&stubJobStore{}, &stubNodeAgent{
		startTaskFunc: func(_, _ string, _ *AgentTaskRequest) (string, error) {
			return "agent-task-2026", nil
		},
	}, ManagerConfig{TypePolicies: map[JobType]TypePolicy{
		"oncpu": {MaxJobsPerHost: maxJobs, MaxTotalJobs: maxJobs},
	}})

	var wg sync.WaitGroup
	var successes atomic.Int32
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := manager.CreateContext(t.Context(), &CreateJobRequest{
				Hostname:  "huatuo-dev",
				Type:      "oncpu",
				AgentTask: &AgentTaskRequest{TraceTimeout: 60},
			})
			if err == nil {
				successes.Add(1)
			}
		}()
	}
	wg.Wait()
	if err := manager.StopAllByTypesContext(t.Context()); err != nil {
		t.Errorf("StopAllByTypesContext() error=%v", err)
	}
	_ = manager.ShutdownContext(t.Context())

	if got := successes.Load(); got > maxJobs {
		t.Errorf("Create() success count=%d, want at most %d", got, maxJobs)
	}
}

func TestManagerStopIsIdempotentAndForwardsForce(t *testing.T) {
	var gotForce atomic.Bool
	nodeAgent := &stubNodeAgent{
		stopTaskFunc: func(_, _ string, force bool) error {
			gotForce.Store(force)
			return nil
		},
	}
	manager := newTestManager(&stubJobStore{}, nodeAgent)
	job := newRunningJob("job-running-2026")
	manager.jobs[job.ID] = job
	manager.jobsByHost[quotaHostKey(job.Hostname, "oncpu")] = 1

	if err := manager.StopContext(t.Context(), job.ID, true); err != nil {
		t.Fatalf("Stop() error=%v, want nil", err)
	}
	if err := manager.StopContext(t.Context(), job.ID, true); err != nil {
		t.Errorf("second Stop() error=%v, want nil", err)
	}
	if got := nodeAgent.stopTaskCalls.Load(); got != 1 {
		t.Errorf("StopTask() call count=%d, want 1", got)
	}
	if !gotForce.Load() {
		t.Error("StopTask() force=false, want true")
	}
}

func TestManagerShutdownIsIdempotent(t *testing.T) {
	manager := newTestManager(&stubJobStore{}, &stubNodeAgent{})

	_ = manager.ShutdownContext(t.Context())
	_ = manager.ShutdownContext(t.Context())
}

func TestManagerCheckAndUpdateJobStatusRejectsMissingResult(t *testing.T) {
	manager := newTestManager(&stubJobStore{}, &stubNodeAgent{
		getTaskStatusFunc: func(_, _ string) (string, *Result, error) {
			return AgentStatusCompleted, nil, nil
		},
	})
	job := newRunningJob("job-running-2026")
	manager.jobs[job.ID] = job
	manager.jobsByHost[quotaHostKey(job.Hostname, "oncpu")] = 1

	_, err := manager.checkAndUpdateJobStatus(t.Context(), job)
	if err == nil || err.Error() != "agent returned completed status without results" {
		t.Errorf("checkAndUpdateJobStatus() error=%v, want missing results error", err)
	}
}

func TestManagerListDoesNotMutateFilter(t *testing.T) {
	manager := newTestManager(&stubJobStore{}, &stubNodeAgent{})
	filter := &JobQuery{Hostname: "huatuo-dev"}
	want := *filter

	if _, err := manager.ListContext(t.Context(), "operator-2026", false, filter); err != nil {
		t.Fatalf("List() error=%v, want nil", err)
	}
	if diff := cmp.Diff(want, *filter); diff != "" {
		t.Errorf("List() filter=%+v, want unchanged %+v", *filter, want)
	}
}

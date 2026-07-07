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
	"errors"
	"strings"
	"testing"
	"time"
)

type stubJobStore struct {
	saveCalls   []*Job
	deleteCalls []string
	getFunc     func(jobID string) (*Job, error)
	listFunc    func(query *JobQuery) ([]*Job, error)
	saveErr     error
	deleteErr   error
}

func (s *stubJobStore) Get(jobID string) (*Job, error) {
	if s.getFunc != nil {
		return s.getFunc(jobID)
	}
	return nil, nil
}

func (s *stubJobStore) Save(job *Job) error {
	s.saveCalls = append(s.saveCalls, job)
	return s.saveErr
}

func (s *stubJobStore) Delete(jobID string) error {
	s.deleteCalls = append(s.deleteCalls, jobID)
	return s.deleteErr
}

func (s *stubJobStore) List(query *JobQuery) ([]*Job, error) {
	if s.listFunc != nil {
		return s.listFunc(query)
	}
	return nil, nil
}

type stubNodeAgent struct {
	startTaskCalls    int
	stopTaskCalls     int
	startTaskFunc     func(host, container string, args *NewAgentTaskReq) (string, error)
	stopTaskFunc      func(host, taskID string, force bool) error
	getTaskStatusFunc func(host, taskID string) (string, *Result, error)
}

func (s *stubNodeAgent) StartTask(host, container string, args *NewAgentTaskReq) (string, error) {
	s.startTaskCalls++
	if s.startTaskFunc != nil {
		return s.startTaskFunc(host, container, args)
	}
	return "", nil
}

func (s *stubNodeAgent) StopTask(host, taskID string, force bool) error {
	s.stopTaskCalls++
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

func newTestManager(storage Store, nodeAgent NodeAgent) *Manager {
	return newManagerWithStore(storage, nodeAgent, ManagerConfig{
		MaxJobsPerHost: 2,
		MaxTotalJobs:   3,
	})
}

func newRunningJob(jobID string) *Job {
	return &Job{
		Type:        "oncpu",
		JobID:       jobID,
		UserName:    "operator-2026",
		UserID:      "operator-2026",
		Container:   "payment-worker",
		Host:        "huatuo-dev",
		AgentTaskID: "agent-task-2026",
		Status:      JobStatusRunning,
		Args: NewAgentTaskReq{
			TracerName:   "oncpu",
			TraceTimeout: 60,
			DataType:     "flamegraph",
		},
		stopChan: make(chan struct{}),
	}
}

// TestManagerCreate tests key branches of Manager.Create, including missing timeout/duration in request, per-host job limit reached, total job limit reached, and successful job creation with field population, task dispatch, and memory index updates.
func TestManagerCreate(t *testing.T) {
	t.Run("timeout or duration required", func(t *testing.T) {
		storage := &stubJobStore{}
		nodeAgent := &stubNodeAgent{}
		manager := newTestManager(storage, nodeAgent)

		job, err := manager.Create(CreateJobRequest{
			UserID:    "operator-2026",
			Container: "payment-worker",
			Host:      "huatuo-dev",
			JobType:   "oncpu",
			Args: &NewAgentTaskReq{
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
		if nodeAgent.startTaskCalls != 0 {
			t.Errorf("StartTask() call count=%d, want 0", nodeAgent.startTaskCalls)
		}
	})

	t.Run("per host limit reached", func(t *testing.T) {
		storage := &stubJobStore{}
		nodeAgent := &stubNodeAgent{}
		manager := newTestManager(storage, nodeAgent)
		manager.jobsByHost.Store("huatuo-dev", 2)

		job, err := manager.Create(CreateJobRequest{
			UserID:    "operator-2026",
			Container: "payment-worker",
			Host:      "huatuo-dev",
			JobType:   "oncpu",
			Args: &NewAgentTaskReq{
				TracerName:   "oncpu",
				TraceTimeout: 60,
				DataType:     "flamegraph",
			},
		})

		if err == nil || !strings.Contains(err.Error(), "maximum number of jobs reached for host") {
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
			MaxJobsPerHost: 3,
			MaxTotalJobs:   2,
		})
		manager.jobs.Store("job-20260101", newRunningJob("job-20260101"))
		manager.jobs.Store("job-20260102", newRunningJob("job-20260102"))

		job, err := manager.Create(CreateJobRequest{
			UserID:    "operator-2026",
			Container: "payment-worker",
			Host:      "huatuo-dev",
			JobType:   "oncpu",
			Args: &NewAgentTaskReq{
				TracerName:   "oncpu",
				TraceTimeout: 60,
				DataType:     "flamegraph",
			},
		})

		if err == nil || !strings.Contains(err.Error(), "maximum number of total jobs reached") {
			t.Errorf("Create() error=%v, want total limit error", err)
		}
		if job != nil {
			t.Errorf("Create() job=%+v, want nil", job)
		}
	})

	t.Run("create success", func(t *testing.T) {
		storage := &stubJobStore{}
		nodeAgent := &stubNodeAgent{
			startTaskFunc: func(host, container string, args *NewAgentTaskReq) (string, error) {
				return "agent-task-2026", nil
			},
		}
		manager := newTestManager(storage, nodeAgent)

		job, err := manager.Create(CreateJobRequest{
			UserID:    "operator-2026",
			Container: "payment-worker",
			Host:      "huatuo-dev",
			JobType:   "oncpu",
			Args: &NewAgentTaskReq{
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
		if !strings.HasPrefix(job.JobID, "id-") {
			t.Errorf("Create() job id=%q, want prefix %q", job.JobID, "id-")
		}
		if job.UserName != "operator-2026" {
			t.Errorf("Create() user name=%q, want %q", job.UserName, "operator-2026")
		}
		if nodeAgent.startTaskCalls != 1 {
			t.Errorf("StartTask() call count=%d, want 1", nodeAgent.startTaskCalls)
		}

		storedJobVal, exists := manager.jobs.Load(job.JobID)
		if !exists {
			t.Errorf("jobs.Load(%q) exists=false, want true", job.JobID)
		} else if storedJobVal.(*Job) != job {
			t.Errorf("jobs.Load(%q) returned unexpected job pointer", job.JobID)
		}

		jobCountVal, exists := manager.jobsByHost.Load("huatuo-dev")
		if !exists {
			t.Errorf("jobsByHost.Load(%q) exists=false, want true", "huatuo-dev")
		} else if jobCountVal.(int) != 1 {
			t.Errorf("jobsByHost.Load(%q)=%d, want 1", "huatuo-dev", jobCountVal.(int))
		}

		if stopErr := manager.Stop(job.JobID, true); stopErr != nil {
			t.Errorf("Stop(%q) error=%v, want nil", job.JobID, stopErr)
		}
	})
}

// TestManagerStop tests Manager.Stop behavior, including returning nil when job doesn't exist, and stopping a running job by dispatching stop command, closing stop channel, updating status to stopped, and removing from memory index.
func TestManagerStop(t *testing.T) {
	t.Run("job not found returns nil", func(t *testing.T) {
		storage := &stubJobStore{}
		nodeAgent := &stubNodeAgent{}
		manager := newTestManager(storage, nodeAgent)

		err := manager.Stop("job-not-found", true)
		if err != nil {
			t.Errorf("Stop() error=%v, want nil", err)
		}
		if nodeAgent.stopTaskCalls != 0 {
			t.Errorf("StopTask() call count=%d, want 0", nodeAgent.stopTaskCalls)
		}
	})

	t.Run("running job is stopped", func(t *testing.T) {
		storage := &stubJobStore{}
		nodeAgent := &stubNodeAgent{}
		manager := newTestManager(storage, nodeAgent)
		job := newRunningJob("job-running-2026")
		manager.jobs.Store(job.JobID, job)
		manager.jobsByHost.Store(job.Host, 1)

		err := manager.Stop(job.JobID, true)
		if err != nil {
			t.Errorf("Stop() error=%v, want nil", err)
		}
		if nodeAgent.stopTaskCalls != 1 {
			t.Errorf("StopTask() call count=%d, want 1", nodeAgent.stopTaskCalls)
		}
		if job.Status != JobStatusStopped {
			t.Errorf("Stop() status=%s, want %s", job.Status, JobStatusStopped)
		}
		if job.Error != "Job stopped by user" {
			t.Errorf("Stop() error message=%q, want %q", job.Error, "Job stopped by user")
		}
		if len(storage.saveCalls) != 1 {
			t.Errorf("storage.Save() call count=%d, want 1", len(storage.saveCalls))
		}

		select {
		case <-job.stopChan:
		default:
			t.Errorf("job.stopChan was not closed")
		}

		if _, exists := manager.jobs.Load(job.JobID); exists {
			t.Errorf("jobs.Load(%q) exists=true, want false", job.JobID)
		}
		if _, exists := manager.jobsByHost.Load(job.Host); exists {
			t.Errorf("jobsByHost.Load(%q) exists=true, want false", job.Host)
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
		manager.jobs.Store(runningJob.JobID, runningJob)

		gotJob, err := manager.Get("job-memory-2026")
		if err != nil {
			t.Errorf("Get() error=%v, want nil", err)
		}
		if gotJob != runningJob {
			t.Errorf("Get() returned unexpected in-memory job pointer")
		}
	})

	t.Run("falls back to store", func(t *testing.T) {
		storage := &stubJobStore{
			getFunc: func(jobID string) (*Job, error) {
				if jobID != "job-archived-2026" {
					t.Errorf("Get() jobID=%q, want %q", jobID, "job-archived-2026")
				}
				return &Job{
					JobID:  "job-archived-2026",
					Status: JobStatusCompleted,
				}, nil
			},
		}
		manager := newTestManager(storage, &stubNodeAgent{})

		gotJob, err := manager.Get("job-archived-2026")
		if err != nil {
			t.Errorf("Get() error=%v, want nil", err)
		}
		if gotJob == nil {
			t.Errorf("Get() returned nil job")
			return
		}
		if gotJob.JobID != "job-archived-2026" {
			t.Errorf("Get() job id=%q, want %q", gotJob.JobID, "job-archived-2026")
		}
	})
}

// TestManagerSave covers Manager.Save delegation: verifies that saves are forwarded to storage and storage errors are propagated unchanged.
func TestManagerSave(t *testing.T) {
	t.Run("save success", func(t *testing.T) {
		storage := &stubJobStore{}
		manager := newTestManager(storage, &stubNodeAgent{})
		jobToSave := newRunningJob("job-save-2026")

		err := manager.Save(jobToSave)
		if err != nil {
			t.Errorf("Save() error=%v, want nil", err)
		}
		if len(storage.saveCalls) != 1 {
			t.Errorf("storage.Save() call count=%d, want 1", len(storage.saveCalls))
		} else if storage.saveCalls[0] != jobToSave {
			t.Errorf("storage.Save() received unexpected job pointer")
		}
	})

	t.Run("save returns store error", func(t *testing.T) {
		saveErr := errors.New("store save failed")
		storage := &stubJobStore{saveErr: saveErr}
		manager := newTestManager(storage, &stubNodeAgent{})

		err := manager.Save(newRunningJob("job-save-error-2026"))
		if !errors.Is(err, saveErr) {
			t.Errorf("Save() error=%v, want %v", err, saveErr)
		}
	})
}

// TestManagerList tests Manager.List query assembly and filtering behavior, including non-admin users only seeing their own jobs, filtering in-memory jobs by container/host/status/type, and passing the same conditions to storage layer then merging results.
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
				Container: "payment-worker",
				Host:      "huatuo-dev",
				Status:    string(JobStatusRunning),
				Type:      "oncpu",
			},
			wantIDs: []string{"job-live-2026", "job-archived-2026"},
			wantQuery: JobQuery{
				UserID:    "operator-2026",
				IsAdmin:   false,
				Container: "payment-worker",
				Host:      "huatuo-dev",
				Status:    string(JobStatusRunning),
				Type:      "oncpu",
			},
		},
		{
			name:    "admin without filter",
			userID:  "operator-2026",
			isAdmin: true,
			wantIDs: []string{"job-live-2026", "job-other-user-2026", "job-archived-2026"},
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
					if *query != tc.wantQuery {
						t.Errorf("List() query=%+v, want %+v", *query, tc.wantQuery)
					}

					return []*Job{
						{
							JobID:     "job-archived-2026",
							UserID:    "operator-2026",
							Container: "payment-worker",
							Host:      "huatuo-dev",
							Status:    JobStatusRunning,
							Type:      "oncpu",
						},
					}, nil
				},
			}
			manager := newTestManager(storage, &stubNodeAgent{})
			manager.jobs.Store("job-live-2026", &Job{
				JobID:     "job-live-2026",
				UserID:    "operator-2026",
				Container: "payment-worker",
				Host:      "huatuo-dev",
				Status:    JobStatusRunning,
				Type:      "oncpu",
			})
			manager.jobs.Store("job-other-user-2026", &Job{
				JobID:     "job-other-user-2026",
				UserID:    "reviewer-2026",
				Container: "db-worker",
				Host:      "huatuo-dev",
				Status:    JobStatusRunning,
				Type:      "offcpu",
			})

			jobs, err := manager.List(tc.userID, tc.isAdmin, tc.filter)
			if err != nil {
				t.Errorf("List() error=%v, want nil", err)
				return
			}
			if len(jobs) != len(tc.wantIDs) {
				t.Errorf("List() len=%d, want %d", len(jobs), len(tc.wantIDs))
			}

			gotIDs := make(map[string]bool, len(jobs))
			for _, job := range jobs {
				gotIDs[job.JobID] = true
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
				if job.Results.URL != "s3://huatuo-region/job-report-2026" {
					t.Errorf("job.Results.URL=%q, want %q", job.Results.URL, "s3://huatuo-region/job-report-2026")
				}
				if len(storage.saveCalls) != 1 {
					t.Errorf("storage.Save() call count=%d, want 1", len(storage.saveCalls))
				}
				if _, exists := manager.jobs.Load(job.JobID); exists {
					t.Errorf("jobs.Load(%q) exists=true, want false", job.JobID)
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
				if job.Error != "Job failed: trace process exited with code 2" {
					t.Errorf("job.Error=%q, want %q", job.Error, "Job failed: trace process exited with code 2")
				}
				if len(storage.saveCalls) != 1 {
					t.Errorf("storage.Save() call count=%d, want 1", len(storage.saveCalls))
				}
				if _, exists := manager.jobsByHost.Load(job.Host); exists {
					t.Errorf("jobsByHost.Load(%q) exists=true, want false", job.Host)
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
				if job.Error != "Job doesn't exist on agent" {
					t.Errorf("job.Error=%q, want %q", job.Error, "Job doesn't exist on agent")
				}
				if len(storage.saveCalls) != 1 {
					t.Errorf("storage.Save() call count=%d, want 1", len(storage.saveCalls))
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
				if len(storage.saveCalls) != 0 {
					t.Errorf("storage.Save() call count=%d, want 0", len(storage.saveCalls))
				}
				if _, exists := manager.jobs.Load(job.JobID); !exists {
					t.Errorf("jobs.Load(%q) exists=false, want true", job.JobID)
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
				if len(storage.saveCalls) != 0 {
					t.Errorf("storage.Save() call count=%d, want 0", len(storage.saveCalls))
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
			manager.jobs.Store(job.JobID, job)
			manager.jobsByHost.Store(job.Host, 1)

			gotStatus, gotErr := manager.checkAndUpdateJobStatus(job)
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
		startTaskFunc: func(host, container string, args *NewAgentTaskReq) (string, error) {
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

	_, err := manager.Create(CreateJobRequest{
		UserID:    "operator-2026",
		Container: "payment-worker",
		Host:      "huatuo-dev",
		JobType:   "oncpu",
		Args: &NewAgentTaskReq{
			TracerName:   "oncpu",
			TraceTimeout: 300,
			DataType:     "flamegraph",
		},
	})
	if err != nil {
		t.Fatalf("Create() error=%v, want nil", err)
	}

	// Simulate manager shutdown while job is still running.
	// Before the fix, this would cause a nil pointer dereference
	// because the defer block called err.Error() when err was nil.
	manager.Shutdown()

	// Wait for monitorJob goroutine to finish processing.
	time.Sleep(300 * time.Millisecond)

	// If we reach here without panic, the test passes.
	// Verify the job was marked as failed with a non-empty error message.
	lastSave := storage.saveCalls[len(storage.saveCalls)-1]
	if lastSave.Status != JobStatusFailed {
		t.Errorf("job.Status=%s, want %s", lastSave.Status, JobStatusFailed)
	}
	if lastSave.Error == "" {
		t.Errorf("job.Error is empty, want non-empty error message")
	}
}

// TestManagerDelete tests Manager.Delete deletion restrictions, including preventing deletion of running jobs in memory, preventing deletion of running jobs in storage, and calling storage layer to delete completed jobs.
func TestManagerDelete(t *testing.T) {
	t.Run("running job in memory cannot be deleted", func(t *testing.T) {
		storage := &stubJobStore{}
		manager := newTestManager(storage, &stubNodeAgent{})
		manager.jobs.Store("job-running-2026", newRunningJob("job-running-2026"))

		err := manager.Delete("job-running-2026")
		if !errors.Is(err, ErrCannotDeleteRunning) {
			t.Errorf("Delete() error=%v, want %v", err, ErrCannotDeleteRunning)
		}
		if len(storage.deleteCalls) != 0 {
			t.Errorf("storage.Delete() call count=%d, want 0", len(storage.deleteCalls))
		}
	})

	t.Run("running job in storage cannot be deleted", func(t *testing.T) {
		storage := &stubJobStore{
			getFunc: func(jobID string) (*Job, error) {
				if jobID != "job-running-2026" {
					t.Errorf("Get() jobID=%q, want %q", jobID, "job-running-2026")
				}
				return &Job{
					JobID:  "job-running-2026",
					Status: JobStatusRunning,
				}, nil
			},
		}
		manager := newTestManager(storage, &stubNodeAgent{})

		err := manager.Delete("job-running-2026")
		if !errors.Is(err, ErrCannotDeleteRunning) {
			t.Errorf("Delete() error=%v, want %v", err, ErrCannotDeleteRunning)
		}
		if len(storage.deleteCalls) != 0 {
			t.Errorf("storage.Delete() call count=%d, want 0", len(storage.deleteCalls))
		}
	})

	t.Run("completed job in storage is deleted", func(t *testing.T) {
		storage := &stubJobStore{
			getFunc: func(jobID string) (*Job, error) {
				if jobID != "job-completed-2026" {
					t.Errorf("Get() jobID=%q, want %q", jobID, "job-completed-2026")
				}
				return &Job{
					JobID:  "job-completed-2026",
					Status: JobStatusCompleted,
				}, nil
			},
		}
		manager := newTestManager(storage, &stubNodeAgent{})

		err := manager.Delete("job-completed-2026")
		if err != nil {
			t.Errorf("Delete() error=%v, want nil", err)
		}
		if len(storage.deleteCalls) != 1 {
			t.Errorf("storage.Delete() call count=%d, want 1", len(storage.deleteCalls))
		} else if storage.deleteCalls[0] != "job-completed-2026" {
			t.Errorf("storage.Delete() condition=%v, want %q", storage.deleteCalls[0], "job-completed-2026")
		}
	})
}

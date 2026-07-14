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
	"fmt"
	"sync"
	"time"

	"huatuo-bamai/internal/log"

	"github.com/google/uuid"
)

// ErrJobCompleted is returned when a job is already completed.
var ErrJobCompleted = fmt.Errorf("job already completed")

// ErrCannotDeleteRunning is returned when trying to delete a running job.
var ErrCannotDeleteRunning = fmt.Errorf("cannot delete running job")

// ManagerConfig holds configuration for the job manager.
type ManagerConfig struct {
	MaxJobsPerHost int
	MaxTotalJobs   int
	// StoreDSN is the SQLite data source name for the job store.
	// Defaults to "jobs.db" when empty.
	StoreDSN string
}

// Manager tracks running jobs in memory and persists terminal states to storage.
type Manager struct {
	jobs       sync.Map // map[string]*Job
	jobsByHost sync.Map // map[string]int
	storage    Store
	nodeAgent  NodeAgent
	stopChan   chan struct{}
	stopOnce   sync.Once
	config     ManagerConfig
}

func NewManager(ctx context.Context, nodeAgent NodeAgent, config ManagerConfig) (*Manager, error) {
	storage, err := newStore(ctx, config.StoreDSN)
	if err != nil {
		return nil, err
	}

	return newManagerWithStore(storage, nodeAgent, config), nil
}

func newManagerWithStore(storage Store, nodeAgent NodeAgent, config ManagerConfig) *Manager {
	return &Manager{
		storage:   storage,
		nodeAgent: nodeAgent,
		stopChan:  make(chan struct{}),
		config:    config,
	}
}

// Shutdown stops the manager and all background jobs
func (m *Manager) Shutdown() {
	var doneChans []chan struct{}
	m.jobs.Range(func(_, value any) bool {
		job := value.(*Job)
		job.runtime.mu.Lock()
		if job.runtime.doneChan != nil {
			doneChans = append(doneChans, job.runtime.doneChan)
		}
		job.runtime.mu.Unlock()
		return true
	})

	m.stopOnce.Do(func() {
		close(m.stopChan)
	})

	for _, doneChan := range doneChans {
		<-doneChan
	}
}

func (m *Manager) Create(req CreateJobRequest) (*Job, error) {
	if req.Args.TraceTimeout == 0 && req.Args.Duration == 0 {
		return nil, fmt.Errorf("trace timeout or duration is required")
	}

	jobCountVal, exists := m.jobsByHost.Load(req.Host)
	if exists {
		jobCount := jobCountVal.(int)
		if jobCount >= m.config.MaxJobsPerHost {
			return nil, fmt.Errorf("maximum number of jobs reached for host %s", req.Host)
		}
	}

	jobCount := 0
	m.jobs.Range(func(_, value any) bool {
		jobCount++
		return true
	})
	if jobCount >= m.config.MaxTotalJobs {
		return nil, fmt.Errorf("maximum number of total jobs reached")
	}

	jobID := fmt.Sprintf("id-%s", uuid.NewString()[:8])
	now := time.Now()
	job := &Job{
		Type:       req.JobType,
		JobID:      jobID,
		UserName:   req.UserID, // Set UserName to be the same as UserID for now
		UserID:     req.UserID,
		Container:  req.Container,
		Host:       req.Host,
		Status:     JobStatusPending,
		StartTime:  now,
		Duration:   req.Args.Duration,
		Timeout:    req.Args.TraceTimeout,
		Args:       *req.Args,
		LastUpdate: now,
		runtime: &jobRuntime{
			stopChan: make(chan struct{}),
			doneChan: make(chan struct{}),
		},
	}

	agentTaskID, err := m.nodeAgent.StartTask(job.Host, job.Container, req.Args)
	if err != nil {
		return nil, fmt.Errorf("start task %s: %w", job.JobID, err)
	}
	job.AgentTaskID = agentTaskID

	m.updateJobStatus(job, JobStatusRunning, "")

	currentCount := 0
	if countVal, exists := m.jobsByHost.Load(req.Host); exists {
		currentCount = countVal.(int)
	}
	m.jobsByHost.Store(req.Host, currentCount+1)

	log.Infof("start task %s on host %s, agent task %s", job.JobID, job.Host, job.AgentTaskID)

	go m.monitorJob(job)

	return snapshotJob(job), nil
}

// Stop stops a job
func (m *Manager) Stop(jobID string, force bool) error {
	jobVal, exists := m.jobs.Load(jobID)
	if !exists {
		// always return nil, because the job may be completed
		return nil
	}
	job := jobVal.(*Job)

	if err := m.stopJob(job, force, JobStatusStopped, "Job stopped by user", true); err != nil {
		return fmt.Errorf("stop task %s: %w", jobID, err)
	}

	log.Infof("Job %s stopped by user", jobID)
	return nil
}

func (m *Manager) Get(jobID string) (*Job, error) {
	jobVal, exists := m.jobs.Load(jobID)
	if exists {
		return snapshotJob(jobVal.(*Job)), nil
	}

	return m.storage.Get(jobID)
}

func (m *Manager) Save(job *Job) error {
	return m.storage.Save(job)
}

func (m *Manager) List(userID string, isAdmin bool, filter *JobQuery) ([]*Job, error) {
	var jobs []*Job

	m.jobs.Range(func(_, value any) bool {
		job := snapshotJob(value.(*Job))

		if !isAdmin && job.UserID != userID {
			return true
		}

		if filter != nil {
			if filter.Container != "" && job.Container != filter.Container {
				return true
			}
			if filter.Host != "" && job.Host != filter.Host {
				return true
			}
			if filter.Status != "" && string(job.Status) != filter.Status {
				return true
			}
			if filter.Type != "" && job.Type != filter.Type {
				return true
			}
		}

		jobs = append(jobs, job)
		return true
	})

	if filter == nil {
		filter = &JobQuery{}
	}
	filter.UserID = userID
	filter.IsAdmin = isAdmin
	storedJobs, err := m.storage.List(filter)
	if err != nil {
		return nil, err
	}

	return append(jobs, storedJobs...), nil
}

func (m *Manager) StopAll() {
	var jobIDs []string

	m.jobs.Range(func(_, value any) bool {
		job := snapshotJob(value.(*Job))
		if job.Status == JobStatusPending || job.Status == JobStatusRunning {
			jobIDs = append(jobIDs, job.JobID)
		}
		return true
	})

	for _, id := range jobIDs {
		if err := m.Stop(id, true); err != nil {
			log.Errorf("Failed to stop agent job %s: %v", id, err)
		}
	}
	log.Infof("admin stopped all jobs(count: %d)", len(jobIDs))
}

func isTerminalJobStatus(status JobStatus) bool {
	return status == JobStatusCompleted || status == JobStatusStopped ||
		status == JobStatusFailed || status == JobStatusTimeout
}

func snapshotJob(job *Job) *Job {
	if job.runtime == nil {
		return snapshotJobLocked(job)
	}

	job.runtime.mu.Lock()
	defer job.runtime.mu.Unlock()

	return snapshotJobLocked(job)
}

func snapshotJobLocked(job *Job) *Job {
	args := job.Args
	args.TracerArgs = append([]string(nil), job.Args.TracerArgs...)

	privateData := make(map[string]any, len(job.PrivateData))
	for key, value := range job.PrivateData {
		privateData[key] = value
	}

	return &Job{
		Type:        job.Type,
		JobID:       job.JobID,
		UserName:    job.UserName,
		UserID:      job.UserID,
		Container:   job.Container,
		Host:        job.Host,
		AgentTaskID: job.AgentTaskID,
		Status:      job.Status,
		Error:       job.Error,
		Duration:    job.Duration,
		Timeout:     job.Timeout,
		StartTime:   job.StartTime,
		EndTime:     job.EndTime,
		Args:        args,
		Results:     job.Results,
		LastUpdate:  job.LastUpdate,
		PrivateData: privateData,
	}
}

func (m *Manager) updateJobStatus(job *Job, status JobStatus, errMesg string) bool {
	job.runtime.mu.Lock()
	defer job.runtime.mu.Unlock()

	return m.updateJobStatusLocked(job, status, errMesg)
}

func (m *Manager) updateJobStatusLocked(job *Job, status JobStatus, errMesg string) bool {
	if isTerminalJobStatus(job.Status) {
		return false
	}

	job.Status = status
	job.LastUpdate = time.Now()

	if isTerminalJobStatus(status) {
		job.EndTime = time.Now()
		job.Error = errMesg

		if err := m.storage.Save(snapshotJobLocked(job)); err != nil {
			log.Errorf("Failed to save job %s: %v", job.JobID, err)
		}

		if countVal, exists := m.jobsByHost.Load(job.Host); exists {
			currentCount := countVal.(int)
			if currentCount <= 1 {
				m.jobsByHost.Delete(job.Host)
			} else {
				m.jobsByHost.Store(job.Host, currentCount-1)
			}
		}

		m.jobs.Delete(job.JobID)
	} else {
		m.jobs.Store(job.JobID, job)
	}

	return true
}

func (m *Manager) stopJob(
	job *Job,
	force bool,
	status JobStatus,
	errMesg string,
	waitForMonitor bool,
) error {
	job.runtime.mu.Lock()
	if isTerminalJobStatus(job.Status) {
		doneChan := job.runtime.doneChan
		job.runtime.mu.Unlock()
		if waitForMonitor && doneChan != nil {
			<-doneChan
		}
		return nil
	}

	if err := m.nodeAgent.StopTask(job.Host, job.AgentTaskID, force); err != nil {
		job.runtime.mu.Unlock()
		return err
	}

	job.runtime.stopOnce.Do(func() {
		if job.runtime.stopChan != nil {
			close(job.runtime.stopChan)
		}
	})
	m.updateJobStatusLocked(job, status, errMesg)
	doneChan := job.runtime.doneChan
	job.runtime.mu.Unlock()

	if waitForMonitor && doneChan != nil {
		<-doneChan
	}

	return nil
}

// checkAndUpdateJobStatus polls the agent for the task's current status and transitions the local job accordingly.
func (m *Manager) checkAndUpdateJobStatus(job *Job) (string, error) {
	job.runtime.mu.Lock()
	defer job.runtime.mu.Unlock()

	if isTerminalJobStatus(job.Status) {
		return string(job.Status), nil
	}

	agentStatus, results, err := m.nodeAgent.GetTaskStatus(job.Host, job.AgentTaskID)
	if err != nil {
		return agentStatus, err
	}

	switch agentStatus {
	case AgentStatusCompleted:
		if results != nil {
			job.Results = *results
		}
		m.updateJobStatusLocked(job, JobStatusCompleted, "")
		return agentStatus, nil
	case AgentStatusFailed:
		resultErr := ""
		if results != nil {
			job.Results = *results
			resultErr = results.Error
		}
		m.updateJobStatusLocked(job, JobStatusFailed, "Job failed: "+resultErr)
		log.Errorf("Job %s failed: %v", job.JobID, resultErr)
		return agentStatus, nil
	case AgentStatusNotExist:
		m.updateJobStatusLocked(job, JobStatusFailed, "Job doesn't exist on agent")
		return agentStatus, nil
	case AgentStatusRunning, AgentStatusPending:
		return agentStatus, nil
	default:
		return agentStatus, nil
	}
}

func (m *Manager) monitorJob(job *Job) {
	var err error
	var status string

	ticker := time.NewTicker(1 * time.Second)
	defer close(job.runtime.doneChan)
	defer ticker.Stop()
	defer func() {
		errMsg := "job interrupted"
		if err != nil {
			errMsg = err.Error()
		}
		if stopErr := m.stopJob(job, true, JobStatusFailed, errMsg, false); stopErr != nil {
			log.Errorf("Failed to stop job %s in defer: %v", job.JobID, stopErr)
		}
	}()

	var timeoutTime, durationEndTime time.Time
	if job.Duration == 0 {
		timeoutTime = job.StartTime.Add(time.Duration(job.Timeout) * time.Second)
	} else {
		durationEndTime = job.StartTime.Add(time.Duration(job.Duration) * time.Second)
	}

	// Counter for status check (every 5 seconds)
	statusCheckCounter := 0

	for {
		select {
		case <-job.runtime.stopChan:
			return
		case <-m.stopChan:
			return
		case <-ticker.C:
			now := time.Now()

			if !timeoutTime.IsZero() && now.After(timeoutTime) {
				log.Infof("Job %s has timed out", job.JobID)

				if status, err = m.checkAndUpdateJobStatus(job); err != nil {
					log.Warnf("Failed to get job status before timeout stop: %v", err)
					return
				} else if status != AgentStatusRunning {
					return
				}

				if stopErr := m.stopJob(
					job,
					true,
					JobStatusTimeout,
					"Job has timed out",
					false,
				); stopErr != nil {
					err = stopErr
					log.Errorf("Failed to stop agent job %s: %v", job.JobID, stopErr)
				}
				return
			}

			if !durationEndTime.IsZero() && now.After(durationEndTime) {
				log.Infof("Job %s duration completed, stopping job", job.JobID)

				if status, err = m.checkAndUpdateJobStatus(job); err != nil {
					log.Warnf("Failed to get job status before stop: %v", err)
					return
				} else if status != AgentStatusRunning {
					return
				}

				if stopErr := m.stopJob(
					job,
					false,
					JobStatusStopped,
					"Job duration completed",
					false,
				); stopErr != nil {
					err = stopErr
					log.Errorf("Failed to stop agent job %s: %v", job.JobID, stopErr)
				} else {
					log.Infof("Job %s stopped by duration", job.JobID)
				}

				return
			}

			// Poll agent status every 5 ticks (5 s).
			statusCheckCounter++
			if statusCheckCounter < 5 {
				continue
			}
			statusCheckCounter = 0

			if status, err = m.checkAndUpdateJobStatus(job); err != nil {
				log.Warnf("Failed to get job status: %v", err)
				return
			} else if status != AgentStatusRunning {
				return
			}
		}
	}
}

// Delete removes the persisted job record; returns ErrCannotDeleteRunning if the job is still active.
func (m *Manager) Delete(jobID string) error {
	if jobVal, exists := m.jobs.Load(jobID); exists {
		job := snapshotJob(jobVal.(*Job))
		if job.Status == JobStatusPending || job.Status == JobStatusRunning {
			return ErrCannotDeleteRunning
		}
	}

	storedJob, err := m.storage.Get(jobID)
	if err != nil {
		return err
	}

	if storedJob.Status == JobStatusPending || storedJob.Status == JobStatusRunning {
		return ErrCannotDeleteRunning
	}

	return m.storage.Delete(jobID)
}

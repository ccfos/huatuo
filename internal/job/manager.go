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
	"fmt"
	"sync"
	"time"

	"huatuo-bamai/internal/log"

	"github.com/google/uuid"
)

// ErrJobCompleted is returned when a job is already completed.
var ErrJobCompleted = errors.New("job already completed")

// ErrCannotDeleteRunning is returned when trying to delete a running job.
var ErrCannotDeleteRunning = errors.New("cannot delete running job")

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
	mu         sync.RWMutex
	stopping   map[string]struct{}
	shutdown   sync.Once
	monitorWG  sync.WaitGroup
	storage    Store
	nodeAgent  NodeAgent
	stopChan   chan struct{}
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
		stopping:  make(map[string]struct{}),
		config:    config,
	}
}

// Shutdown stops the manager and waits for its background monitors.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	m.shutdown.Do(func() {
		close(m.stopChan)
	})
	m.mu.Unlock()
	m.monitorWG.Wait()
}

func (m *Manager) Create(req CreateJobRequest) (*Job, error) {
	if req.Args == nil {
		return nil, errors.New("job arguments are required")
	}
	if req.Args.TraceTimeout == 0 && req.Args.Duration == 0 {
		return nil, errors.New("trace timeout or duration is required")
	}

	jobID := fmt.Sprintf("id-%s", uuid.NewString()[:8])
	now := time.Now()
	job := &Job{
		Type:        req.JobType,
		JobID:       jobID,
		UserName:    req.UserID, // Set UserName to be the same as UserID for now
		UserID:      req.UserID,
		Container:   req.Container,
		Host:        req.Host,
		Status:      JobStatusPending,
		StartTime:   now,
		Duration:    req.Args.Duration,
		Timeout:     req.Args.TraceTimeout,
		Args:        *req.Args,
		LastUpdate:  now,
		stopChan:    make(chan struct{}),
		PrivateData: cloneJobPrivateData(req.PrivateData),
	}

	m.mu.Lock()
	select {
	case <-m.stopChan:
		m.mu.Unlock()
		return nil, errors.New("job manager is shutting down")
	default:
	}
	if m.hostJobCount(req.Host) >= m.config.MaxJobsPerHost {
		m.mu.Unlock()
		return nil, fmt.Errorf("maximum number of jobs reached for host %s", req.Host)
	}
	if m.jobCount() >= m.config.MaxTotalJobs {
		m.mu.Unlock()
		return nil, errors.New("maximum number of total jobs reached")
	}
	m.jobs.Store(jobID, job)
	m.jobsByHost.Store(req.Host, m.hostJobCount(req.Host)+1)
	m.monitorWG.Add(1)
	m.mu.Unlock()

	agentTaskID, err := m.nodeAgent.StartTask(job.Host, job.Container, req.Args)
	if err != nil {
		m.rollbackJob(job)
		m.monitorWG.Done()
		return nil, fmt.Errorf("start task %s: %w", job.JobID, err)
	}

	m.mu.Lock()
	job.AgentTaskID = agentTaskID
	job.Status = JobStatusRunning
	job.LastUpdate = time.Now()
	m.mu.Unlock()

	log.WithField("job_id", job.JobID).WithField("host", job.Host).
		Info("started agent task")
	go func() {
		defer m.monitorWG.Done()
		m.monitorJob(job)
	}()

	return cloneJob(job), nil
}

// Stop stops a job
func (m *Manager) Stop(jobID string, force bool) error {
	m.mu.Lock()
	jobVal, exists := m.jobs.Load(jobID)
	if !exists {
		m.mu.Unlock()
		return nil
	}
	job := jobVal.(*Job)
	if _, exists := m.stopping[jobID]; exists {
		m.mu.Unlock()
		return nil
	}
	m.stopping[jobID] = struct{}{}
	host, agentTaskID := job.Host, job.AgentTaskID
	m.mu.Unlock()

	err := m.nodeAgent.StopTask(host, agentTaskID, force)
	if err != nil {
		m.mu.Lock()
		delete(m.stopping, jobID)
		m.mu.Unlock()
		return fmt.Errorf("stop task %s: %w", jobID, err)
	}

	m.finishJob(job, JobStatusStopped, "job stopped by user", nil)
	log.WithField("job_id", jobID).Info("stopped job by user")
	return nil
}

func (m *Manager) Get(jobID string) (*Job, error) {
	m.mu.RLock()
	jobVal, exists := m.jobs.Load(jobID)
	if exists {
		job := cloneJob(jobVal.(*Job))
		m.mu.RUnlock()
		return job, nil
	}
	m.mu.RUnlock()

	return m.storage.Get(jobID)
}

func (m *Manager) Save(job *Job) error {
	return m.storage.Save(job)
}

func (m *Manager) List(userID string, isAdmin bool, filter *JobQuery) ([]*Job, error) {
	var jobs []*Job

	m.mu.RLock()
	m.jobs.Range(func(_, value any) bool {
		job := value.(*Job)

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

		jobs = append(jobs, cloneJob(job))
		return true
	})
	m.mu.RUnlock()

	query := JobQuery{}
	if filter != nil {
		query = *filter
	}
	query.UserID = userID
	query.IsAdmin = isAdmin
	storedJobs, err := m.storage.List(&query)
	if err != nil {
		return nil, err
	}

	return append(jobs, storedJobs...), nil
}

func (m *Manager) StopAll() {
	var jobIDs []string

	m.mu.RLock()
	m.jobs.Range(func(_, value any) bool {
		job := value.(*Job)
		if job.Status == JobStatusPending || job.Status == JobStatusRunning {
			jobIDs = append(jobIDs, job.JobID)
		}
		return true
	})
	m.mu.RUnlock()

	for _, id := range jobIDs {
		if err := m.Stop(id, true); err != nil {
			log.WithError(err).WithField("job_id", id).Error("failed to stop agent job")
		}
	}
	log.WithField("count", len(jobIDs)).Info("stopped all jobs")
}

func (m *Manager) finishJob(job *Job, status JobStatus, errMessage string, result *Result) {
	m.mu.Lock()
	if _, exists := m.jobs.Load(job.JobID); !exists {
		m.mu.Unlock()
		return
	}
	now := time.Now()
	job.Status = status
	job.LastUpdate = now
	job.EndTime = now
	job.Error = errMessage
	if result != nil {
		job.Results = *result
	}
	select {
	case <-job.stopChan:
	default:
		close(job.stopChan)
	}
	m.jobs.Delete(job.JobID)
	delete(m.stopping, job.JobID)
	count := m.hostJobCount(job.Host)
	if count <= 1 {
		m.jobsByHost.Delete(job.Host)
	} else {
		m.jobsByHost.Store(job.Host, count-1)
	}
	snapshot := cloneJob(job)
	m.mu.Unlock()

	if err := m.storage.Save(snapshot); err != nil {
		log.WithError(err).WithField("job_id", job.JobID).Error("failed to save job")
	}
}

func (m *Manager) jobCount() int {
	count := 0
	m.jobs.Range(func(_, _ any) bool {
		count++
		return true
	})
	return count
}

func (m *Manager) hostJobCount(host string) int {
	count, exists := m.jobsByHost.Load(host)
	if !exists {
		return 0
	}
	return count.(int)
}

func (m *Manager) rollbackJob(job *Job) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.jobs.Delete(job.JobID)
	count := m.hostJobCount(job.Host)
	if count <= 1 {
		m.jobsByHost.Delete(job.Host)
		return
	}
	m.jobsByHost.Store(job.Host, count-1)
}

func (m *Manager) jobIsActive(jobID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	_, exists := m.jobs.Load(jobID)
	return exists
}

func (m *Manager) stopAgent(job *Job, force bool) error {
	if err := m.nodeAgent.StopTask(job.Host, job.AgentTaskID, force); err != nil {
		return fmt.Errorf("stop task %s: %w", job.JobID, err)
	}
	return nil
}

// checkAndUpdateJobStatus polls the agent for the task's current status and transitions the local job accordingly.
func (m *Manager) checkAndUpdateJobStatus(job *Job) (string, error) {
	agentStatus, results, err := m.nodeAgent.GetTaskStatus(job.Host, job.AgentTaskID)
	if err != nil {
		return agentStatus, err
	}

	switch agentStatus {
	case AgentStatusCompleted:
		if results == nil {
			return agentStatus, errors.New("agent returned completed status without results")
		}
		m.finishJob(job, JobStatusCompleted, "", results)
		return agentStatus, nil
	case AgentStatusFailed:
		if results == nil {
			return agentStatus, errors.New("agent returned failed status without results")
		}
		m.finishJob(job, JobStatusFailed, "job failed: "+results.Error, results)
		log.WithField("job_id", job.JobID).WithField("agent_error", results.Error).
			Error("job failed")
		return agentStatus, nil
	case AgentStatusNotExist:
		m.finishJob(job, JobStatusFailed, "job does not exist on agent", nil)
		return agentStatus, nil
	case AgentStatusRunning, AgentStatusPending:
		return agentStatus, nil
	default:
		return agentStatus, fmt.Errorf("agent returned unknown task status %q", agentStatus)
	}
}

func (m *Manager) monitorJob(job *Job) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

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
		case <-job.stopChan:
			return
		case <-m.stopChan:
			if err := m.stopAgent(job, true); err != nil {
				log.WithError(err).WithField("job_id", job.JobID).
					Error("failed to stop job during shutdown")
			}
			m.finishJob(job, JobStatusFailed, "job interrupted by manager shutdown", nil)
			return
		case <-ticker.C:
			now := time.Now()

			if !timeoutTime.IsZero() && now.After(timeoutTime) {
				status, err := m.checkAndUpdateJobStatus(job)
				if err != nil {
					m.failMonitoredJob(job, err)
					return
				}
				if status != AgentStatusRunning {
					return
				}

				if err := m.stopAgent(job, true); err != nil {
					m.failMonitoredJob(job, err)
					return
				}
				m.finishJob(job, JobStatusTimeout, "job timed out", nil)
				return
			}

			if !durationEndTime.IsZero() && now.After(durationEndTime) {
				status, err := m.checkAndUpdateJobStatus(job)
				if err != nil {
					m.failMonitoredJob(job, err)
					return
				}
				if status != AgentStatusRunning {
					return
				}

				if err := m.stopAgent(job, false); err != nil {
					m.failMonitoredJob(job, err)
					return
				}
				status, err = m.checkAndUpdateJobStatus(job)
				if err != nil {
					m.failMonitoredJob(job, err)
					return
				}
				if status == AgentStatusRunning {
					m.finishJob(job, JobStatusStopped, "job stopped after duration completed", nil)
				}
				return
			}

			// Poll agent status every 5 ticks (5 s).
			statusCheckCounter++
			if statusCheckCounter < 5 {
				continue
			}
			statusCheckCounter = 0

			status, err := m.checkAndUpdateJobStatus(job)
			if err != nil {
				m.failMonitoredJob(job, err)
				return
			}
			if status != AgentStatusRunning && status != AgentStatusPending {
				return
			}
		}
	}
}

func (m *Manager) failMonitoredJob(job *Job, cause error) {
	if !m.jobIsActive(job.JobID) {
		return
	}
	if err := m.stopAgent(job, true); err != nil {
		log.WithError(err).WithField("job_id", job.JobID).
			Error("failed to stop job after monitor error")
	}
	m.finishJob(job, JobStatusFailed, cause.Error(), nil)
}

// Delete removes the persisted job record; returns ErrCannotDeleteRunning if the job is still active.
func (m *Manager) Delete(jobID string) error {
	m.mu.RLock()
	if jobVal, exists := m.jobs.Load(jobID); exists {
		job := jobVal.(*Job)
		if job.Status == JobStatusPending || job.Status == JobStatusRunning {
			m.mu.RUnlock()
			return ErrCannotDeleteRunning
		}
	}
	m.mu.RUnlock()

	storedJob, err := m.storage.Get(jobID)
	if err != nil {
		return err
	}

	if storedJob.Status == JobStatusPending || storedJob.Status == JobStatusRunning {
		return ErrCannotDeleteRunning
	}

	return m.storage.Delete(jobID)
}

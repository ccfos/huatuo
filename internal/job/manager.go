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

// ManagerConfig holds configuration for the job manager
type ManagerConfig struct {
	MaxJobsPerHost int
	MaxTotalJobs   int
}

// Manager manages jobs
type Manager struct {
	jobs       sync.Map // map[string]*Job
	jobsByHost sync.Map // map[string]int
	storage    Storage
	nodeAgent  NodeAgent
	stopChan   chan struct{}
	config     ManagerConfig
}

// NewManager creates a new job manager
func NewManager(storage Storage, nodeAgent NodeAgent, config ManagerConfig) *Manager {
	return &Manager{
		storage:   storage,
		nodeAgent: nodeAgent,
		stopChan:  make(chan struct{}),
		config:    config,
	}
}

// Shutdown stops the manager and all background jobs
func (m *Manager) Shutdown() {
	close(m.stopChan)
}

// Create creates a new job
func (m *Manager) Create(req CreateJobRequest) (*Job, error) {
	if req.Args.TraceTimeout == 0 && req.Args.Duration == 0 {
		return nil, fmt.Errorf("trace timeout or duration is required")
	}

	// Check per-host limit
	jobCountVal, exists := m.jobsByHost.Load(req.Host)
	if exists {
		jobCount := jobCountVal.(int)
		if jobCount >= m.config.MaxJobsPerHost {
			return nil, fmt.Errorf("maximum number of jobs reached for host %s", req.Host)
		}
	}

	// Check total jobs limit
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
		stopChan:   make(chan struct{}),
	}

	agentTaskID, err := m.nodeAgent.StartTask(job.Host, job.Container, req.Args)
	if err != nil {
		return nil, fmt.Errorf("start task %s: %w", job.JobID, err)
	}
	job.AgentTaskID = agentTaskID

	log.Infof("start task %s on host %s, task info: %+v", job.JobID, job.Host, job)

	go m.monitorJob(job)

	m.updateJobStatus(job, JobStatusRunning, "")

	m.jobs.Store(jobID, job)

	currentCount := 0
	if countVal, exists := m.jobsByHost.Load(req.Host); exists {
		currentCount = countVal.(int)
	}
	m.jobsByHost.Store(req.Host, currentCount+1)

	return job, nil
}

// Stop stops a job
func (m *Manager) Stop(jobID string, force bool) error {
	jobVal, exists := m.jobs.Load(jobID)
	if !exists {
		// always return nil, because the job may be completed
		return nil
	}
	job := jobVal.(*Job)

	err := m.nodeAgent.StopTask(job.Host, job.AgentTaskID, false)
	if err != nil {
		return fmt.Errorf("stop task %s: %w", jobID, err)
	}

	close(job.stopChan)
	m.updateJobStatus(job, JobStatusStopped, "Job stopped by user")
	log.Infof("Job %s stopped by user", jobID)
	return nil
}

// Get gets a job by ID
func (m *Manager) Get(jobID string) (*Job, error) {
	jobVal, exists := m.jobs.Load(jobID)
	if exists {
		return jobVal.(*Job), nil
	}

	query := JobQuery{
		JobID:   jobID,
		IsAdmin: true, // No user filter when looking up by ID
	}
	var storedJob *Job
	err := m.storage.Search(query, &storedJob)
	if err != nil {
		return nil, err
	}

	return storedJob, nil
}

// Save saves a job to storage
func (m *Manager) Save(job *Job) error {
	return m.storage.Save(job)
}

// List lists jobs based on filters
func (m *Manager) List(userID string, isAdmin bool, filter *JobQuery) ([]*Job, error) {
	var jobs []*Job

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

		jobs = append(jobs, job)
		return true
	})

	// Get jobs from storage
	var storedJobs []*Job
	if filter == nil {
		filter = &JobQuery{}
	}
	filter.UserID = userID
	filter.IsAdmin = isAdmin
	err := m.storage.Search(filter, &storedJobs)
	if err != nil {
		return nil, err
	}

	return append(jobs, storedJobs...), nil
}

// StopAll stops all running jobs
func (m *Manager) StopAll() {
	var jobIDs []string

	m.jobs.Range(func(_, value any) bool {
		job := value.(*Job)
		if job.Status == JobStatusRunning || job.Status == JobStatusPending {
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

// updateJobStatus updates the status of a job
func (m *Manager) updateJobStatus(job *Job, status JobStatus, errMesg string) {
	job.Status = status
	job.LastUpdate = time.Now()

	if status == JobStatusCompleted || status == JobStatusStopped || status == JobStatusFailed || status == JobStatusTimeout {
		job.EndTime = time.Now()
		job.Error = errMesg

		if err := m.storage.Save(job); err != nil {
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
}

// checkAndUpdateJobStatus check agent job status and update local job status
// return:
//
//	status - agent status
//	err - query error
func (m *Manager) checkAndUpdateJobStatus(job *Job) (string, error) {
	agentStatus, results, err := m.nodeAgent.GetTaskStatus(job.Host, job.AgentTaskID)
	if err != nil {
		return agentStatus, err
	}

	switch agentStatus {
	case AgentStatusCompleted:
		job.Results = *results
		m.updateJobStatus(job, JobStatusCompleted, "")
		return agentStatus, nil
	case AgentStatusFailed:
		job.Results = *results
		m.updateJobStatus(job, JobStatusFailed, "Job failed: "+results.Error)
		log.Errorf("Job %s failed: %v", job.JobID, results.Error)
		return agentStatus, nil
	case AgentStatusNotExist:
		m.updateJobStatus(job, JobStatusFailed, "Job doesn't exist on agent")
		return agentStatus, nil
	case AgentStatusRunning, AgentStatusPending:
		return agentStatus, nil
	default:
		return agentStatus, nil
	}
}

// monitorJob monitors the status of a single job
func (m *Manager) monitorJob(job *Job) {
	var err error
	var status string

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	defer func() {
		// if job is still running when monitorJob exits, it means some error happened,
		// try to stop job and set job status to failed
		if job.Status == JobStatusRunning {
			if stopErr := m.Stop(job.JobID, true); stopErr != nil {
				log.Errorf("Failed to stop job %s in defer: %v", job.JobID, stopErr)
			}
			m.updateJobStatus(job, JobStatusFailed, err.Error())
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
		case <-job.stopChan:
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

				if err := m.Stop(job.JobID, true); err != nil {
					log.Errorf("Failed to stop agent job %s: %v", job.JobID, err)
				}
				m.updateJobStatus(job, JobStatusTimeout, "Job has timed out")
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

				if err := m.Stop(job.JobID, false); err != nil {
					log.Errorf("Failed to stop agent job %s: %v", job.JobID, err)
				}
				if status, err = m.checkAndUpdateJobStatus(job); err != nil {
					log.Warnf("Failed to get job status after stop: %v", err)
				} else if status != AgentStatusRunning {
					log.Infof("Job %s stopped by duration", job.JobID)
				}

				return
			}

			// Increment counter and check if it's time to query status (every 5 seconds)
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

// Delete deletes a job record by ID, but only if it's not running
func (m *Manager) Delete(jobID string) error {
	if jobVal, exists := m.jobs.Load(jobID); exists {
		job := jobVal.(*Job)
		if job.Status == JobStatusPending || job.Status == JobStatusRunning {
			return ErrCannotDeleteRunning
		}
	}

	query := JobQuery{
		JobID:   jobID,
		IsAdmin: true, // No user filter when looking up by ID
	}
	var storedJob *Job
	err := m.storage.Search(query, &storedJob)
	if err != nil {
		return err
	}

	if storedJob.Status == JobStatusPending || storedJob.Status == JobStatusRunning {
		return ErrCannotDeleteRunning
	}

	return m.storage.Delete(jobID)
}

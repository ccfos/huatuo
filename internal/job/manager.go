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
	TypePolicies   map[string]TypePolicy
	// StoreDSN is the SQLite data source name for the job store.
	// Defaults to "jobs.db" when empty.
	StoreDSN string
}

// TypePolicy assigns a job type to a quota group.
type TypePolicy struct {
	Group          string
	MaxJobsPerHost int
	MaxTotalJobs   int
}

// Manager tracks running jobs in memory and persists terminal states to storage.
type Manager struct {
	jobs        sync.Map // map[string]*Job
	jobsByHost  sync.Map // map[string]int
	mu          sync.RWMutex
	stopping    map[string]struct{}
	shutdown    sync.Once
	shutdownErr error
	monitorWG   sync.WaitGroup
	storage     Store
	nodeAgent   NodeAgent
	stopChan    chan struct{}
	config      ManagerConfig
	ctx         context.Context
	cancel      context.CancelFunc
}

func NewManager(ctx context.Context, nodeAgent NodeAgent, config ManagerConfig) (*Manager, error) {
	storage, err := newStore(ctx, config.StoreDSN)
	if err != nil {
		return nil, err
	}

	manager := newManagerWithStore(storage, nodeAgent, config)
	manager.cancel()
	manager.ctx, manager.cancel = context.WithCancel(ctx)
	return manager, nil
}

func newManagerWithStore(storage Store, nodeAgent NodeAgent, config ManagerConfig) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		storage:   storage,
		nodeAgent: nodeAgent,
		stopChan:  make(chan struct{}),
		stopping:  make(map[string]struct{}),
		config:    config,
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Shutdown stops the manager and waits for its background monitors.
func (m *Manager) Shutdown() {
	_ = m.ShutdownContext(context.Background())
}

// ShutdownContext stops monitors, waits for them, and closes the job store.
func (m *Manager) ShutdownContext(ctx context.Context) error {
	m.shutdown.Do(func() {
		stopErr := m.stopAllByTypes(ctx, nil)
		m.mu.Lock()
		close(m.stopChan)
		m.cancel()
		m.mu.Unlock()

		done := make(chan struct{})
		go func() {
			m.monitorWG.Wait()
			close(done)
		}()
		var waitErr error
		select {
		case <-done:
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
		var closeErr error
		if store, ok := m.storage.(contextStore); ok {
			closeErr = store.Close(ctx)
		}
		m.shutdownErr = errors.Join(stopErr, waitErr, closeErr)
	})
	return m.shutdownErr
}

func (m *Manager) Create(req *CreateJobRequest) (*Job, error) {
	return m.CreateContext(context.Background(), req)
}

// CreateContext creates a job and propagates cancellation to the node agent.
func (m *Manager) CreateContext(ctx context.Context, req *CreateJobRequest) (*Job, error) {
	if req == nil {
		return nil, errors.New("job request is required")
	}
	if req.AgentTask == nil {
		return nil, errors.New("job arguments are required")
	}
	if req.AgentTask.TraceTimeout == 0 && req.AgentTask.Duration == 0 {
		return nil, errors.New("trace timeout or duration is required")
	}

	jobID := fmt.Sprintf("id-%s", uuid.NewString()[:8])
	now := time.Now()
	job := &Job{
		Type:         req.Type,
		ID:           jobID,
		Username:     req.UserID, // Username mirrors UserID until identity names are distinct.
		UserID:       req.UserID,
		ContainerID:  req.ContainerID,
		Hostname:     req.Hostname,
		Status:       JobStatusPending,
		StartTime:    now,
		Duration:     req.AgentTask.Duration,
		TraceTimeout: req.AgentTask.TraceTimeout,
		AgentTask:    *req.AgentTask,
		UpdatedAt:    now,
		stopCh:       make(chan struct{}),
		PrivateData:  cloneJobPrivateData(req.PrivateData),
	}

	m.mu.Lock()
	select {
	case <-m.stopChan:
		m.mu.Unlock()
		return nil, errors.New("job manager is shutting down")
	default:
	}
	policy, err := m.policyFor(req.Type)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if m.hostJobCount(req.Hostname, policy.Group) >= policy.MaxJobsPerHost {
		m.mu.Unlock()
		if policy.Group == "" {
			return nil, fmt.Errorf("%w: maximum number of jobs reached for host %s", ErrQuotaExceeded, req.Hostname)
		}
		return nil, fmt.Errorf("%w: maximum number of %s jobs reached for host %s", ErrQuotaExceeded, policy.Group, req.Hostname)
	}
	if m.jobCount(policy.Group) >= policy.MaxTotalJobs {
		m.mu.Unlock()
		if policy.Group == "" {
			return nil, fmt.Errorf("%w: maximum number of total jobs reached", ErrQuotaExceeded)
		}
		return nil, fmt.Errorf("%w: maximum number of total %s jobs reached", ErrQuotaExceeded, policy.Group)
	}
	m.jobs.Store(jobID, job)
	hostKey := quotaHostKey(req.Hostname, policy.Group)
	m.jobsByHost.Store(hostKey, m.hostJobCount(req.Hostname, policy.Group)+1)
	m.monitorWG.Add(1)
	m.mu.Unlock()

	agentTaskID, err := m.startTask(ctx, job.Hostname, job.ContainerID, req.AgentTask)
	if err != nil {
		m.rollbackJob(job)
		m.monitorWG.Done()
		return nil, fmt.Errorf("start task %s: %w", job.ID, err)
	}

	m.mu.Lock()
	job.AgentTaskID = agentTaskID
	job.Status = JobStatusRunning
	job.UpdatedAt = time.Now()
	m.mu.Unlock()

	log.WithField("job_id", job.ID).WithField("host", job.Hostname).
		Info("started agent task")
	go func() {
		defer m.monitorWG.Done()
		m.monitorJob(job)
	}()

	return cloneJob(job), nil
}

// Stop stops a job
func (m *Manager) Stop(jobID string, force bool) error {
	return m.StopContext(context.Background(), jobID, force)
}

// StopContext stops a job and propagates cancellation to the node agent.
func (m *Manager) StopContext(ctx context.Context, jobID string, force bool) error {
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
	host, agentTaskID := job.Hostname, job.AgentTaskID
	m.mu.Unlock()

	err := m.stopTask(ctx, host, agentTaskID, force)
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
	return m.GetContext(context.Background(), jobID)
}

// GetContext gets a job while propagating cancellation to storage.
func (m *Manager) GetContext(ctx context.Context, jobID string) (*Job, error) {
	m.mu.RLock()
	jobVal, exists := m.jobs.Load(jobID)
	if exists {
		job := cloneJob(jobVal.(*Job))
		m.mu.RUnlock()
		return job, nil
	}
	m.mu.RUnlock()

	return m.storeGet(ctx, jobID)
}

// GetByTypes returns a job only when it belongs to one of the expected types.
func (m *Manager) GetByTypes(jobID string, expectedTypes ...string) (*Job, error) {
	return m.GetByTypesContext(context.Background(), jobID, expectedTypes...)
}

// GetByTypesContext gets an expected job type while propagating cancellation.
func (m *Manager) GetByTypesContext(ctx context.Context, jobID string, expectedTypes ...string) (*Job, error) {
	jobResult, err := m.GetContext(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if !hasJobType(jobResult.Type, expectedTypes) {
		return nil, ErrNotFound
	}
	return jobResult, nil
}

func (m *Manager) Save(job *Job) error {
	return m.storeSave(context.Background(), job)
}

func (m *Manager) List(userID string, isAdmin bool, filter *JobQuery) ([]*Job, error) {
	return m.ListContext(context.Background(), userID, isAdmin, filter)
}

// ListContext lists jobs while propagating cancellation to storage.
func (m *Manager) ListContext(ctx context.Context, userID string, isAdmin bool, filter *JobQuery) ([]*Job, error) {
	var jobs []*Job

	m.mu.RLock()
	m.jobs.Range(func(_, value any) bool {
		job := value.(*Job)

		if !isAdmin && job.UserID != userID {
			return true
		}

		if filter != nil {
			if filter.ContainerID != "" && job.ContainerID != filter.ContainerID {
				return true
			}
			if filter.Hostname != "" && job.Hostname != filter.Hostname {
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
	storedJobs, err := m.storeList(ctx, &query)
	if err != nil {
		return nil, err
	}

	return append(jobs, storedJobs...), nil
}

func (m *Manager) StopAll() {
	_ = m.stopAllByTypes(m.ctx, nil)
}

// StopAllByTypes stops active jobs belonging to the expected types.
func (m *Manager) StopAllByTypes(expectedTypes ...string) {
	_ = m.stopAllByTypes(m.ctx, expectedTypes)
}

// StopAllByTypesContext stops expected active jobs and returns all stop failures.
func (m *Manager) StopAllByTypesContext(ctx context.Context, expectedTypes ...string) error {
	return m.stopAllByTypes(ctx, expectedTypes)
}

func (m *Manager) stopAllByTypes(ctx context.Context, expectedTypes []string) error {
	var jobIDs []string
	var errs []error

	m.mu.RLock()
	m.jobs.Range(func(_, value any) bool {
		job := value.(*Job)
		if len(expectedTypes) > 0 && !hasJobType(job.Type, expectedTypes) {
			return true
		}
		if job.Status == JobStatusPending || job.Status == JobStatusRunning {
			jobIDs = append(jobIDs, job.ID)
		}
		return true
	})
	m.mu.RUnlock()

	for _, id := range jobIDs {
		if err := m.StopContext(ctx, id, true); err != nil {
			log.WithError(err).WithField("job_id", id).Error("failed to stop agent job")
			errs = append(errs, fmt.Errorf("stop job %s: %w", id, err))
		}
	}
	log.WithField("count", len(jobIDs)).Info("stopped all jobs")
	return errors.Join(errs...)
}

// StopByTypes stops a job only when it belongs to one of the expected types.
func (m *Manager) StopByTypes(jobID string, force bool, expectedTypes ...string) error {
	return m.StopByTypesContext(context.Background(), jobID, force, expectedTypes...)
}

// StopByTypesContext stops an expected job type while propagating cancellation.
func (m *Manager) StopByTypesContext(ctx context.Context, jobID string, force bool, expectedTypes ...string) error {
	if _, err := m.GetByTypesContext(ctx, jobID, expectedTypes...); err != nil {
		return err
	}
	return m.StopContext(ctx, jobID, force)
}

func (m *Manager) finishJob(job *Job, status JobStatus, errMessage string, result *Result) {
	m.mu.Lock()
	if _, exists := m.jobs.Load(job.ID); !exists {
		m.mu.Unlock()
		return
	}
	now := time.Now()
	job.Status = status
	job.UpdatedAt = now
	job.EndTime = now
	job.ErrorMessage = errMessage
	if result != nil {
		job.Result = *result
	}
	select {
	case <-job.stopCh:
	default:
		close(job.stopCh)
	}
	m.jobs.Delete(job.ID)
	delete(m.stopping, job.ID)
	policy, policyErr := m.policyFor(job.Type)
	hostKey := quotaHostKey(job.Hostname, policy.Group)
	count := m.hostJobCount(job.Hostname, policy.Group)
	if count <= 1 {
		m.jobsByHost.Delete(hostKey)
	} else {
		m.jobsByHost.Store(hostKey, count-1)
	}
	snapshot := cloneJob(job)
	m.mu.Unlock()
	if policyErr != nil {
		log.WithError(policyErr).WithField("job_id", job.ID).Error("failed to release job quota")
	}

	if err := m.storeSave(m.ctx, snapshot); err != nil {
		log.WithError(err).WithField("job_id", job.ID).Error("failed to save job")
	}
}

func (m *Manager) jobCount(group string) int {
	count := 0
	m.jobs.Range(func(_, value any) bool {
		job := value.(*Job)
		policy, err := m.policyFor(job.Type)
		if err == nil && (group == "" || policy.Group == group) {
			count++
		}
		return true
	})
	return count
}

func (m *Manager) hostJobCount(host, group string) int {
	count, exists := m.jobsByHost.Load(quotaHostKey(host, group))
	if !exists {
		return 0
	}
	return count.(int)
}

func (m *Manager) policyFor(jobType string) (TypePolicy, error) {
	if len(m.config.TypePolicies) == 0 {
		return TypePolicy{
			MaxJobsPerHost: m.config.MaxJobsPerHost,
			MaxTotalJobs:   m.config.MaxTotalJobs,
		}, nil
	}
	policy, ok := m.config.TypePolicies[jobType]
	if !ok {
		return TypePolicy{}, fmt.Errorf("%w: %q", ErrUnsupportedJobType, jobType)
	}
	if policy.Group == "" {
		policy.Group = jobType
	}
	return policy, nil
}

func quotaHostKey(host, group string) string {
	if group == "" {
		return host
	}
	return group + "\x00" + host
}

func (m *Manager) rollbackJob(job *Job) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.jobs.Delete(job.ID)
	policy, err := m.policyFor(job.Type)
	if err != nil {
		log.WithError(err).WithField("job_id", job.ID).Error("failed to release job quota")
		return
	}
	hostKey := quotaHostKey(job.Hostname, policy.Group)
	count := m.hostJobCount(job.Hostname, policy.Group)
	if count <= 1 {
		m.jobsByHost.Delete(hostKey)
		return
	}
	m.jobsByHost.Store(hostKey, count-1)
}

func (m *Manager) jobIsActive(jobID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	_, exists := m.jobs.Load(jobID)
	return exists
}

func (m *Manager) stopAgent(job *Job, force bool) error {
	if err := m.stopTask(m.ctx, job.Hostname, job.AgentTaskID, force); err != nil {
		return fmt.Errorf("stop task %s: %w", job.ID, err)
	}
	return nil
}

// checkAndUpdateJobStatus polls the agent for the task's current status and transitions the local job accordingly.
func (m *Manager) checkAndUpdateJobStatus(job *Job) (string, error) {
	agentStatus, results, err := m.getTaskStatus(m.ctx, job.Hostname, job.AgentTaskID)
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
		log.WithField("job_id", job.ID).WithField("agent_error", results.Error).
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
		timeoutTime = job.StartTime.Add(time.Duration(job.TraceTimeout) * time.Second)
	} else {
		durationEndTime = job.StartTime.Add(time.Duration(job.Duration) * time.Second)
	}

	// Counter for status check (every 5 seconds)
	statusCheckCounter := 0

	for {
		select {
		case <-job.stopCh:
			return
		case <-m.stopChan:
			if err := m.stopAgent(job, true); err != nil {
				log.WithError(err).WithField("job_id", job.ID).
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
	if !m.jobIsActive(job.ID) {
		return
	}
	if err := m.stopAgent(job, true); err != nil {
		log.WithError(err).WithField("job_id", job.ID).
			Error("failed to stop job after monitor error")
	}
	m.finishJob(job, JobStatusFailed, cause.Error(), nil)
}

// Delete removes the persisted job record; returns ErrCannotDeleteRunning if the job is still active.
func (m *Manager) Delete(jobID string) error {
	return m.DeleteContext(context.Background(), jobID)
}

// DeleteContext deletes a completed job while propagating cancellation to storage.
func (m *Manager) DeleteContext(ctx context.Context, jobID string) error {
	m.mu.RLock()
	if jobVal, exists := m.jobs.Load(jobID); exists {
		job := jobVal.(*Job)
		if job.Status == JobStatusPending || job.Status == JobStatusRunning {
			m.mu.RUnlock()
			return ErrCannotDeleteRunning
		}
	}
	m.mu.RUnlock()

	storedJob, err := m.storeGet(ctx, jobID)
	if err != nil {
		return err
	}

	if storedJob.Status == JobStatusPending || storedJob.Status == JobStatusRunning {
		return ErrCannotDeleteRunning
	}

	return m.storeDelete(ctx, jobID)
}

// DeleteByTypes deletes a job only when it belongs to one of the expected types.
func (m *Manager) DeleteByTypes(jobID string, expectedTypes ...string) error {
	return m.DeleteByTypesContext(context.Background(), jobID, expectedTypes...)
}

// DeleteByTypesContext deletes an expected job type while propagating cancellation.
func (m *Manager) DeleteByTypesContext(ctx context.Context, jobID string, expectedTypes ...string) error {
	if _, err := m.GetByTypesContext(ctx, jobID, expectedTypes...); err != nil {
		return err
	}
	return m.DeleteContext(ctx, jobID)
}

func hasJobType(jobType string, expectedTypes []string) bool {
	for _, expectedType := range expectedTypes {
		if jobType == expectedType {
			return true
		}
	}
	return false
}

func (m *Manager) startTask(ctx context.Context, host, container string, req *AgentTaskRequest) (string, error) {
	if agent, ok := m.nodeAgent.(contextNodeAgent); ok {
		return agent.StartTaskContext(ctx, host, container, req)
	}
	return m.nodeAgent.StartTask(host, container, req)
}

func (m *Manager) stopTask(ctx context.Context, host, taskID string, force bool) error {
	if agent, ok := m.nodeAgent.(contextNodeAgent); ok {
		return agent.StopTaskContext(ctx, host, taskID, force)
	}
	return m.nodeAgent.StopTask(host, taskID, force)
}

func (m *Manager) getTaskStatus(ctx context.Context, host, taskID string) (string, *Result, error) {
	if agent, ok := m.nodeAgent.(contextNodeAgent); ok {
		return agent.GetTaskStatusContext(ctx, host, taskID)
	}
	return m.nodeAgent.GetTaskStatus(host, taskID)
}

func (m *Manager) storeGet(ctx context.Context, jobID string) (*Job, error) {
	if store, ok := m.storage.(contextStore); ok {
		return store.GetContext(ctx, jobID)
	}
	return m.storage.Get(jobID)
}

func (m *Manager) storeSave(ctx context.Context, jobEntity *Job) error {
	if store, ok := m.storage.(contextStore); ok {
		return store.SaveContext(ctx, jobEntity)
	}
	return m.storage.Save(jobEntity)
}

func (m *Manager) storeDelete(ctx context.Context, jobID string) error {
	if store, ok := m.storage.(contextStore); ok {
		return store.DeleteContext(ctx, jobID)
	}
	return m.storage.Delete(jobID)
}

func (m *Manager) storeList(ctx context.Context, query *JobQuery) ([]*Job, error) {
	if store, ok := m.storage.(contextStore); ok {
		return store.ListContext(ctx, query)
	}
	return m.storage.List(query)
}

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
	"sync/atomic"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/storage/driver"

	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"
)

// ErrJobCompleted is returned when a job is already completed.
var ErrJobCompleted = errors.New("job already completed")

// ErrCannotDeleteRunning is returned when trying to delete a running job.
var ErrCannotDeleteRunning = errors.New("cannot delete running job")

// ManagerConfig holds configuration for the job manager.
type ManagerConfig struct {
	TypePolicies map[JobType]TypePolicy
	// ShutdownConcurrency bounds simultaneous Agent stop requests.
	ShutdownConcurrency int
	// StoreDSN is the SQLite data source name for the job store.
	// Defaults to "jobs.db" when empty.
	StoreDSN                 string
	StatusPollInterval       time.Duration
	MaxConsecutivePollErrors int
}

// TypePolicy assigns a job type to a quota group.
type TypePolicy struct {
	Group          string
	MaxJobsPerHost int
	MaxTotalJobs   int
}

// Manager tracks running jobs in memory and persists terminal states to storage.
type Manager struct {
	jobs                map[string]*Job
	jobsByHost          map[string]int
	mu                  sync.RWMutex
	stopping            map[string]struct{}
	finishing           map[string]chan struct{}
	shutdownMu          sync.Mutex
	shuttingDown        bool
	shutdownDone        chan struct{}
	shutdownErr         error
	monitorWG           sync.WaitGroup
	storage             Store
	nodeAgent           NodeAgent
	stopChan            chan struct{}
	config              ManagerConfig
	quotaRejections     atomic.Uint64
	persistenceFailures atomic.Uint64
	recoveredJobs       atomic.Uint64
	shutdownIncomplete  atomic.Uint64
}

// ActiveJobStat contains one active job metric bucket.
type ActiveJobStat struct {
	Type   JobType
	Status JobStatus
	Count  int
}

// ManagerStats is a point-in-time snapshot of manager metrics.
type ManagerStats struct {
	Active              []ActiveJobStat
	QuotaRejections     uint64
	PersistenceFailures uint64
	RecoveredJobs       uint64
	ShutdownIncomplete  uint64
}

func NewManager(ctx context.Context, nodeAgent NodeAgent, config ManagerConfig) (*Manager, error) {
	if nodeAgent == nil {
		return nil, errors.New("node agent is required")
	}
	if err := validateManagerConfig(config); err != nil {
		return nil, err
	}
	storage, err := newStore(ctx, config.StoreDSN)
	if err != nil {
		return nil, err
	}

	manager := newManagerWithStore(storage, nodeAgent, config)
	if err := manager.recoverJobs(ctx); err != nil {
		_ = storage.Close(ctx)
		return nil, fmt.Errorf("recover jobs: %w", err)
	}
	return manager, nil
}

func validateManagerConfig(config ManagerConfig) error {
	if len(config.TypePolicies) == 0 {
		return errors.New("job type policies are required")
	}
	groups := make(map[string]TypePolicy)
	for jobType, policy := range config.TypePolicies {
		if jobType == "" {
			return errors.New("job type policy has an empty type")
		}
		if policy.MaxJobsPerHost <= 0 || policy.MaxTotalJobs <= 0 {
			return fmt.Errorf("job type %q quotas must be greater than zero", jobType)
		}
		group := policy.Group
		if group == "" {
			group = string(jobType)
		}
		if existing, ok := groups[group]; ok &&
			(existing.MaxJobsPerHost != policy.MaxJobsPerHost || existing.MaxTotalJobs != policy.MaxTotalJobs) {
			return fmt.Errorf("job quota group %q has inconsistent limits", group)
		}
		groups[group] = policy
	}
	return nil
}

func newManagerWithStore(storage Store, nodeAgent NodeAgent, config ManagerConfig) *Manager {
	if config.ShutdownConcurrency <= 0 {
		config.ShutdownConcurrency = 16
	}
	if config.StatusPollInterval <= 0 {
		config.StatusPollInterval = 5 * time.Second
	}
	if config.MaxConsecutivePollErrors <= 0 {
		config.MaxConsecutivePollErrors = 3
	}
	policies := make(map[JobType]TypePolicy, len(config.TypePolicies))
	for jobType, policy := range config.TypePolicies {
		policies[jobType] = policy
	}
	config.TypePolicies = policies
	return &Manager{
		storage:      storage,
		nodeAgent:    nodeAgent,
		jobs:         make(map[string]*Job),
		jobsByHost:   make(map[string]int),
		stopChan:     make(chan struct{}),
		stopping:     make(map[string]struct{}),
		finishing:    make(map[string]chan struct{}),
		shutdownDone: make(chan struct{}),
		config:       config,
	}
}

func (m *Manager) recoverJobs(ctx context.Context) error {
	jobs, err := m.storage.List(ctx, &JobQuery{
		Statuses: []JobStatus{JobStatusPending, JobStatusRunning},
	})
	if err != nil {
		return err
	}

	m.mu.Lock()
	for _, recoveredJob := range jobs {
		policy, policyErr := m.policyFor(recoveredJob.Type)
		if policyErr != nil {
			m.mu.Unlock()
			return fmt.Errorf("job %s: %w", recoveredJob.ID, policyErr)
		}
		if recoveredJob.AgentTaskID == "" {
			recoveredJob.AgentTaskID = recoveredJob.ID
		}
		recoveredJob.AgentTask.RequestID = recoveredJob.ID
		recoveredJob.stopCh = make(chan struct{})
		m.jobs[recoveredJob.ID] = recoveredJob
		hostKey := quotaHostKey(recoveredJob.Hostname, policy.Group)
		m.jobsByHost[hostKey]++
		m.monitorWG.Add(1)
		go func(jobToMonitor *Job) {
			defer m.monitorWG.Done()
			m.monitorJob(context.WithoutCancel(ctx), jobToMonitor)
		}(recoveredJob)
	}
	m.recoveredJobs.Add(uint64(len(jobs)))
	m.mu.Unlock()
	return nil
}

// ShutdownContext stops monitors, waits for them, and closes the job store.
func (m *Manager) ShutdownContext(ctx context.Context) error {
	m.shutdownMu.Lock()
	if m.shuttingDown {
		done := m.shutdownDone
		m.shutdownMu.Unlock()
		select {
		case <-done:
			m.shutdownMu.Lock()
			defer m.shutdownMu.Unlock()
			return m.shutdownErr
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.shuttingDown = true
	m.shutdownMu.Unlock()

	stopErr := m.stopAllByTypes(ctx, nil)
	m.mu.Lock()
	close(m.stopChan)
	m.mu.Unlock()

	closeCtx := context.WithoutCancel(ctx)
	go func() {
		m.monitorWG.Wait()
		closeErr := m.storage.Close(closeCtx)
		m.shutdownMu.Lock()
		m.shutdownErr = errors.Join(stopErr, closeErr)
		close(m.shutdownDone)
		m.shutdownMu.Unlock()
	}()

	select {
	case <-m.shutdownDone:
		m.shutdownMu.Lock()
		defer m.shutdownMu.Unlock()
		return m.shutdownErr
	case <-ctx.Done():
		m.shutdownIncomplete.Add(1)
		return errors.Join(stopErr, ctx.Err())
	}
}

// Stats returns current active job counts and cumulative failure counters.
func (m *Manager) Stats() ManagerStats {
	type bucket struct {
		jobType JobType
		status  JobStatus
	}
	m.mu.RLock()
	counts := make(map[bucket]int)
	for _, activeJob := range m.jobs {
		counts[bucket{jobType: activeJob.Type, status: activeJob.Status}]++
	}
	m.mu.RUnlock()
	active := make([]ActiveJobStat, 0, len(counts))
	for key, count := range counts {
		active = append(active, ActiveJobStat{Type: key.jobType, Status: key.status, Count: count})
	}
	return ManagerStats{
		Active:              active,
		QuotaRejections:     m.quotaRejections.Load(),
		PersistenceFailures: m.persistenceFailures.Load(),
		RecoveredJobs:       m.recoveredJobs.Load(),
		ShutdownIncomplete:  m.shutdownIncomplete.Load(),
	}
}

// Ready verifies that the durable job store is available.
func (m *Manager) Ready(ctx context.Context) error {
	if _, err := m.storage.Count(ctx, &JobQuery{}); err != nil {
		return fmt.Errorf("job store readiness: %w", err)
	}
	return nil
}

// CreateContext creates a job and propagates cancellation to the node agent.
func (m *Manager) CreateContext(ctx context.Context, req *CreateJobRequest) (*Job, error) {
	return m.createContext(ctx, req, 3)
}

func (m *Manager) createContext(ctx context.Context, req *CreateJobRequest, idAttempts int) (*Job, error) {
	if req == nil {
		return nil, errors.New("job request is required")
	}
	if req.AgentTask == nil {
		return nil, errors.New("job arguments are required")
	}
	if req.AgentTask.TraceTimeout == 0 && req.AgentTask.Duration == 0 {
		return nil, errors.New("trace timeout or duration is required")
	}

	jobID := "id-" + uuid.NewString()
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
	job.AgentTaskID = job.ID
	job.AgentTask.RequestID = job.ID

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
		m.quotaRejections.Add(1)
		if policy.Group == "" {
			return nil, fmt.Errorf("%w: maximum number of jobs reached for host %s", ErrQuotaExceeded, req.Hostname)
		}
		return nil, fmt.Errorf("%w: maximum number of %s jobs reached for host %s", ErrQuotaExceeded, policy.Group, req.Hostname)
	}
	if m.jobCount(policy.Group) >= policy.MaxTotalJobs {
		m.mu.Unlock()
		m.quotaRejections.Add(1)
		if policy.Group == "" {
			return nil, fmt.Errorf("%w: maximum number of total jobs reached", ErrQuotaExceeded)
		}
		return nil, fmt.Errorf("%w: maximum number of total %s jobs reached", ErrQuotaExceeded, policy.Group)
	}
	m.jobs[jobID] = job
	hostKey := quotaHostKey(req.Hostname, policy.Group)
	m.jobsByHost[hostKey] = m.hostJobCount(req.Hostname, policy.Group) + 1
	m.monitorWG.Add(1)
	m.mu.Unlock()

	if err := m.storage.Create(ctx, cloneJob(job)); err != nil {
		m.persistenceFailures.Add(1)
		m.rollbackJob(job)
		m.monitorWG.Done()
		if errors.Is(err, driver.ErrAlreadyExists) && idAttempts > 1 {
			return m.createContext(ctx, req, idAttempts-1)
		}
		return nil, fmt.Errorf("persist pending job %s: %w", job.ID, err)
	}

	agentTask := job.AgentTask
	agentTaskID, err := m.startTask(ctx, job.Hostname, job.ContainerID, &agentTask)
	if err != nil {
		if errors.Is(err, ErrAgentDispatchUncertain) {
			log.WithError(err).WithField("job_id", job.ID).
				Warn("agent task dispatch result is uncertain")
			go func() {
				defer m.monitorWG.Done()
				m.monitorJob(context.WithoutCancel(ctx), job)
			}()
			return cloneJob(job), nil
		}
		finishErr := m.finishJob(ctx, job, JobStatusFailed, err.Error(), nil)
		if finishErr == nil {
			m.monitorWG.Done()
		} else {
			go m.monitorCreationFailure(context.WithoutCancel(ctx), job)
		}
		return nil, errors.Join(fmt.Errorf("start task %s: %w", job.ID, err), finishErr)
	}

	m.mu.Lock()
	job.AgentTaskID = agentTaskID
	job.Status = JobStatusRunning
	job.UpdatedAt = time.Now()
	runningSnapshot := cloneJob(job)
	m.mu.Unlock()
	if err := m.storage.Save(ctx, runningSnapshot); err != nil {
		m.persistenceFailures.Add(1)
		stopErr := m.stopTask(ctx, job.Hostname, agentTaskID, true)
		finishErr := m.finishJob(ctx, job, JobStatusFailed, "failed to persist running job", nil)
		if finishErr == nil {
			m.monitorWG.Done()
		} else {
			go m.monitorCreationFailure(context.WithoutCancel(ctx), job)
		}
		return nil, errors.Join(fmt.Errorf("persist running job %s: %w", job.ID, err), stopErr, finishErr)
	}

	log.WithField("job_id", job.ID).WithField("host", job.Hostname).
		Info("started agent task")
	go func() {
		defer m.monitorWG.Done()
		m.monitorJob(context.WithoutCancel(ctx), job)
	}()

	return cloneJob(job), nil
}

func (m *Manager) monitorCreationFailure(ctx context.Context, job *Job) {
	defer m.monitorWG.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-job.stopCh:
			return
		case <-m.stopChan:
			return
		case <-ticker.C:
			if err := m.finishJob(ctx, job, JobStatusFailed, "job creation failed", nil); err == nil {
				return
			}
		}
	}
}

// Stop stops a job
// StopContext stops a job and propagates cancellation to the node agent.
func (m *Manager) StopContext(ctx context.Context, jobID string, force bool) error {
	m.mu.Lock()
	job, exists := m.jobs[jobID]
	if !exists {
		m.mu.Unlock()
		storedJob, err := m.storage.Get(ctx, jobID)
		if errors.Is(err, ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		if storedJob == nil {
			return nil
		}
		if storedJob.Status == JobStatusPending || storedJob.Status == JobStatusRunning {
			return errors.New("active job is not loaded")
		}
		return nil
	}
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

	if err := m.finishJob(ctx, job, JobStatusStopped, "job stopped by user", nil); err != nil {
		m.mu.Lock()
		delete(m.stopping, jobID)
		m.mu.Unlock()
		return err
	}
	log.WithField("job_id", jobID).Info("stopped job by user")
	return nil
}

// GetContext gets a job while propagating cancellation to storage.
func (m *Manager) GetContext(ctx context.Context, jobID string) (*Job, error) {
	m.mu.RLock()
	job, exists := m.jobs[jobID]
	if exists {
		job := cloneJob(job)
		m.mu.RUnlock()
		return job, nil
	}
	m.mu.RUnlock()

	return m.storage.Get(ctx, jobID)
}

// GetByTypesContext gets an expected job type while propagating cancellation.
func (m *Manager) GetByTypesContext(ctx context.Context, jobID string, expectedTypes ...JobType) (*Job, error) {
	jobResult, err := m.GetContext(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if !hasJobType(jobResult.Type, expectedTypes) {
		return nil, ErrNotFound
	}
	return jobResult, nil
}

// ListContext lists jobs while propagating cancellation to storage.
func (m *Manager) ListContext(ctx context.Context, userID string, isAdmin bool, filter *JobQuery) ([]*Job, error) {
	query := JobQuery{}
	if filter != nil {
		query = *filter
	}
	query.UserID = userID
	query.IsAdmin = isAdmin
	return m.storage.List(ctx, &query)
}

// ListPageContext lists a stable storage-backed page of jobs.
func (m *Manager) ListPageContext(ctx context.Context, userID string, isAdmin bool, query *JobQuery) (*JobPage, error) {
	filter := JobQuery{}
	if query != nil {
		filter = *query
	}
	filter.UserID = userID
	filter.IsAdmin = isAdmin
	items, err := m.storage.List(ctx, &filter)
	if err != nil {
		return nil, err
	}
	filter.Limit = 0
	filter.Offset = 0
	total, err := m.storage.Count(ctx, &filter)
	if err != nil {
		return nil, err
	}
	return &JobPage{Items: items, Total: total}, nil
}

// StopAllByTypesContext stops expected active jobs and returns all stop failures.
func (m *Manager) StopAllByTypesContext(ctx context.Context, expectedTypes ...JobType) error {
	return m.stopAllByTypes(ctx, expectedTypes)
}

func (m *Manager) stopAllByTypes(ctx context.Context, expectedTypes []JobType) error {
	var jobIDs []string
	var errs []error

	m.mu.RLock()
	for _, job := range m.jobs {
		if len(expectedTypes) > 0 && !hasJobType(job.Type, expectedTypes) {
			continue
		}
		if job.Status == JobStatusPending || job.Status == JobStatusRunning {
			jobIDs = append(jobIDs, job.ID)
		}
	}
	m.mu.RUnlock()

	var errsMu sync.Mutex
	var group errgroup.Group
	group.SetLimit(m.config.ShutdownConcurrency)
	for _, id := range jobIDs {
		jobID := id
		group.Go(func() error {
			if err := m.StopContext(ctx, jobID, true); err != nil {
				errsMu.Lock()
				errs = append(errs, fmt.Errorf("stop job %s: %w", jobID, err))
				errsMu.Unlock()
			}
			return nil
		})
	}
	_ = group.Wait()
	log.WithField("count", len(jobIDs)).Info("stopped all jobs")
	return errors.Join(errs...)
}

// StopByTypesContext stops an expected job type while propagating cancellation.
func (m *Manager) StopByTypesContext(ctx context.Context, jobID string, force bool, expectedTypes ...JobType) error {
	if _, err := m.GetByTypesContext(ctx, jobID, expectedTypes...); err != nil {
		return err
	}
	return m.StopContext(ctx, jobID, force)
}

func (m *Manager) finishJob(ctx context.Context, job *Job, status JobStatus, errMessage string, result *Result) error {
	for {
		m.mu.Lock()
		wait, inProgress := m.finishing[job.ID]
		if !inProgress {
			m.finishing[job.ID] = make(chan struct{})
			m.mu.Unlock()
			break
		}
		m.mu.Unlock()
		select {
		case <-wait:
			if !m.jobIsActive(job.ID) {
				return nil
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	defer func() {
		m.mu.Lock()
		close(m.finishing[job.ID])
		delete(m.finishing, job.ID)
		m.mu.Unlock()
	}()

	m.mu.Lock()
	if _, exists := m.jobs[job.ID]; !exists {
		m.mu.Unlock()
		return nil
	}
	now := time.Now()
	snapshot := cloneJob(job)
	snapshot.Status = status
	snapshot.UpdatedAt = now
	snapshot.EndTime = now
	snapshot.ErrorMessage = errMessage
	if result != nil {
		snapshot.Result = *result
	}
	m.mu.Unlock()

	if err := m.storage.Save(ctx, snapshot); err != nil {
		m.persistenceFailures.Add(1)
		return errors.Join(ErrPersistence, fmt.Errorf("persist terminal job %s: %w", job.ID, err))
	}

	m.mu.Lock()
	current, exists := m.jobs[job.ID]
	if !exists {
		m.mu.Unlock()
		return nil
	}
	current.Status = snapshot.Status
	current.UpdatedAt = snapshot.UpdatedAt
	current.EndTime = snapshot.EndTime
	current.ErrorMessage = snapshot.ErrorMessage
	current.Result = snapshot.Result
	select {
	case <-current.stopCh:
	default:
		close(current.stopCh)
	}
	delete(m.jobs, job.ID)
	delete(m.stopping, job.ID)
	policy, policyErr := m.policyFor(current.Type)
	if policyErr != nil {
		m.mu.Unlock()
		return fmt.Errorf("release job quota: %w", policyErr)
	}
	hostKey := quotaHostKey(job.Hostname, policy.Group)
	count := m.hostJobCount(job.Hostname, policy.Group)
	if count <= 1 {
		delete(m.jobsByHost, hostKey)
	} else {
		m.jobsByHost[hostKey] = count - 1
	}
	m.mu.Unlock()
	return nil
}

func (m *Manager) jobCount(group string) int {
	count := 0
	for _, job := range m.jobs {
		policy, err := m.policyFor(job.Type)
		if err == nil && (group == "" || policy.Group == group) {
			count++
		}
	}
	return count
}

func (m *Manager) hostJobCount(host, group string) int {
	count, exists := m.jobsByHost[quotaHostKey(host, group)]
	if !exists {
		return 0
	}
	return count
}

func (m *Manager) policyFor(jobType JobType) (TypePolicy, error) {
	policy, ok := m.config.TypePolicies[jobType]
	if !ok {
		return TypePolicy{}, fmt.Errorf("%w: %q", ErrUnsupportedJobType, jobType)
	}
	if policy.Group == "" {
		policy.Group = string(jobType)
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

	delete(m.jobs, job.ID)
	policy, err := m.policyFor(job.Type)
	if err != nil {
		log.WithError(err).WithField("job_id", job.ID).Error("failed to release job quota")
		return
	}
	hostKey := quotaHostKey(job.Hostname, policy.Group)
	count := m.hostJobCount(job.Hostname, policy.Group)
	if count <= 1 {
		delete(m.jobsByHost, hostKey)
		return
	}
	m.jobsByHost[hostKey] = count - 1
}

func (m *Manager) jobIsActive(jobID string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	_, exists := m.jobs[jobID]
	return exists
}

func (m *Manager) stopAgent(ctx context.Context, job *Job, force bool) error {
	if err := m.stopTask(ctx, job.Hostname, job.AgentTaskID, force); err != nil {
		return fmt.Errorf("stop task %s: %w", job.ID, err)
	}
	return nil
}

// checkAndUpdateJobStatus polls the agent for the task's current status and transitions the local job accordingly.
func (m *Manager) checkAndUpdateJobStatus(ctx context.Context, job *Job) (string, error) {
	m.mu.RLock()
	jobSnapshot := cloneJob(job)
	m.mu.RUnlock()
	agentStatus, results, err := m.getTaskStatus(ctx, jobSnapshot.Hostname, jobSnapshot.AgentTaskID)
	if err != nil {
		return agentStatus, err
	}

	switch agentStatus {
	case AgentStatusCompleted:
		if results == nil {
			return agentStatus, errors.New("agent returned completed status without results")
		}
		return agentStatus, m.finishJob(ctx, job, JobStatusCompleted, "", results)
	case AgentStatusFailed:
		if results == nil {
			return agentStatus, errors.New("agent returned failed status without results")
		}
		if err := m.finishJob(ctx, job, JobStatusFailed, "job failed: "+results.Error, results); err != nil {
			return agentStatus, err
		}
		log.WithField("job_id", job.ID).WithField("agent_error", results.Error).
			Error("job failed")
		return agentStatus, nil
	case AgentStatusNotExist:
		if jobSnapshot.Status == JobStatusPending {
			return m.restartPendingJob(ctx, job)
		}
		return agentStatus, m.finishJob(ctx, job, JobStatusFailed, "job does not exist on agent", nil)
	case AgentStatusRunning, AgentStatusPending:
		if jobSnapshot.Status == JobStatusPending && agentStatus == AgentStatusRunning {
			if err := m.markJobRunning(ctx, job); err != nil {
				return agentStatus, err
			}
		}
		return agentStatus, nil
	default:
		return agentStatus, fmt.Errorf("agent returned unknown task status %q", agentStatus)
	}
}

func (m *Manager) restartPendingJob(ctx context.Context, job *Job) (string, error) {
	task := job.AgentTask
	task.RequestID = job.ID
	taskID, err := m.startTask(ctx, job.Hostname, job.ContainerID, &task)
	if err != nil {
		return AgentStatusNotExist, fmt.Errorf("restart pending task: %w", err)
	}
	m.mu.Lock()
	job.AgentTaskID = taskID
	m.mu.Unlock()
	if err := m.markJobRunning(ctx, job); err != nil {
		return AgentStatusRunning, err
	}
	return AgentStatusRunning, nil
}

func (m *Manager) markJobRunning(ctx context.Context, job *Job) error {
	m.mu.Lock()
	if _, exists := m.jobs[job.ID]; !exists {
		m.mu.Unlock()
		return nil
	}
	job.Status = JobStatusRunning
	job.UpdatedAt = time.Now()
	snapshot := cloneJob(job)
	m.mu.Unlock()
	if err := m.storage.Save(ctx, snapshot); err != nil {
		m.persistenceFailures.Add(1)
		return errors.Join(ErrPersistence, fmt.Errorf("persist running job %s: %w", job.ID, err))
	}
	return nil
}

func (m *Manager) monitorJob(ctx context.Context, job *Job) {
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
	statusCheckTicks := max(1, int(m.config.StatusPollInterval/time.Second))
	consecutivePollErrors := 0

	for {
		select {
		case <-job.stopCh:
			return
		case <-m.stopChan:
			if err := m.stopAgent(ctx, job, true); err != nil {
				log.WithError(err).WithField("job_id", job.ID).
					Error("failed to stop job during shutdown")
			}
			if err := m.finishJob(ctx, job, JobStatusFailed, "job interrupted by manager shutdown", nil); err != nil {
				log.WithError(err).WithField("job_id", job.ID).Error("failed to persist interrupted job")
			}
			return
		case <-ticker.C:
			now := time.Now()

			if !timeoutTime.IsZero() && now.After(timeoutTime) {
				status, err := m.checkAndUpdateJobStatus(ctx, job)
				if err != nil {
					if errors.Is(err, ErrPersistence) {
						log.WithError(err).WithField("job_id", job.ID).Error("failed to persist job state")
						continue
					}
					m.failMonitoredJob(ctx, job, err)
					return
				}
				if status != AgentStatusRunning {
					return
				}

				if err := m.stopAgent(ctx, job, true); err != nil {
					m.failMonitoredJob(ctx, job, err)
					return
				}
				if err := m.finishJob(ctx, job, JobStatusTimeout, "job timed out", nil); err != nil {
					log.WithError(err).WithField("job_id", job.ID).Error("failed to persist timed out job")
				}
				return
			}

			if !durationEndTime.IsZero() && now.After(durationEndTime) {
				status, err := m.checkAndUpdateJobStatus(ctx, job)
				if err != nil {
					if errors.Is(err, ErrPersistence) {
						log.WithError(err).WithField("job_id", job.ID).Error("failed to persist job state")
						continue
					}
					m.failMonitoredJob(ctx, job, err)
					return
				}
				if status != AgentStatusRunning {
					return
				}

				if err := m.stopAgent(ctx, job, false); err != nil {
					m.failMonitoredJob(ctx, job, err)
					return
				}
				status, err = m.checkAndUpdateJobStatus(ctx, job)
				if err != nil {
					m.failMonitoredJob(ctx, job, err)
					return
				}
				if status == AgentStatusRunning {
					if err := m.finishJob(ctx, job, JobStatusStopped, "job stopped after duration completed", nil); err != nil {
						log.WithError(err).WithField("job_id", job.ID).Error("failed to persist stopped job")
					}
				}
				return
			}

			// Poll agent status every 5 ticks (5 s).
			statusCheckCounter++
			if statusCheckCounter < statusCheckTicks {
				continue
			}
			statusCheckCounter = 0

			status, err := m.checkAndUpdateJobStatus(ctx, job)
			if err != nil {
				if errors.Is(err, ErrPersistence) {
					log.WithError(err).WithField("job_id", job.ID).Error("failed to persist job state")
					continue
				}
				consecutivePollErrors++
				if consecutivePollErrors < m.config.MaxConsecutivePollErrors {
					log.WithError(err).WithField("job_id", job.ID).Warn("agent status check failed")
					continue
				}
				m.failMonitoredJob(ctx, job, err)
				return
			}
			consecutivePollErrors = 0
			if status != AgentStatusRunning && status != AgentStatusPending {
				return
			}
		}
	}
}

func (m *Manager) failMonitoredJob(ctx context.Context, job *Job, cause error) {
	if !m.jobIsActive(job.ID) {
		return
	}
	if err := m.stopAgent(ctx, job, true); err != nil {
		log.WithError(err).WithField("job_id", job.ID).
			Error("failed to stop job after monitor error")
	}
	if err := m.finishJob(ctx, job, JobStatusFailed, cause.Error(), nil); err != nil {
		log.WithError(err).WithField("job_id", job.ID).Error("failed to persist failed job")
	}
}

// DeleteContext deletes a completed job while propagating cancellation to storage.
func (m *Manager) DeleteContext(ctx context.Context, jobID string) error {
	m.mu.RLock()
	if job, exists := m.jobs[jobID]; exists {
		if job.Status == JobStatusPending || job.Status == JobStatusRunning {
			m.mu.RUnlock()
			return ErrCannotDeleteRunning
		}
	}
	m.mu.RUnlock()

	storedJob, err := m.storage.Get(ctx, jobID)
	if err != nil {
		return err
	}

	if storedJob.Status == JobStatusPending || storedJob.Status == JobStatusRunning {
		return ErrCannotDeleteRunning
	}

	return m.storage.Delete(ctx, jobID)
}

// DeleteByTypesContext deletes an expected job type while propagating cancellation.
func (m *Manager) DeleteByTypesContext(ctx context.Context, jobID string, expectedTypes ...JobType) error {
	if _, err := m.GetByTypesContext(ctx, jobID, expectedTypes...); err != nil {
		return err
	}
	return m.DeleteContext(ctx, jobID)
}

func hasJobType(jobType JobType, expectedTypes []JobType) bool {
	for _, expectedType := range expectedTypes {
		if jobType == expectedType {
			return true
		}
	}
	return false
}

func (m *Manager) startTask(ctx context.Context, host, container string, req *AgentTaskRequest) (string, error) {
	return m.nodeAgent.StartTaskContext(ctx, host, container, req)
}

func (m *Manager) stopTask(ctx context.Context, host, taskID string, force bool) error {
	return m.nodeAgent.StopTaskContext(ctx, host, taskID, force)
}

func (m *Manager) getTaskStatus(ctx context.Context, host, taskID string) (string, *Result, error) {
	return m.nodeAgent.GetTaskStatusContext(ctx, host, taskID)
}

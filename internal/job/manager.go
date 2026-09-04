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
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"

	"huatuo-bamai/internal/log"

	"github.com/google/uuid"
)

const (
	defaultStatusPollInterval         = 5 * time.Second
	defaultPendingTimeout             = 30 * time.Second
	defaultCompletionGracePeriod      = 60 * time.Second
	defaultNodeUnavailableGracePeriod = 30 * time.Second
	defaultJobRetentionPeriod         = 30 * 24 * time.Hour
	jobCleanupInterval                = time.Hour
	jobCleanupBatchSize               = 1000
	maxJobPageSize                    = 1000
)

// Policy limits active Jobs for one service Kind.
type Policy struct {
	MaxJobsPerHost int
	MaxTotalJobs   int
}

// ManagerConfig contains Apiserver-owned persistence, quota, and lifecycle policy.
type ManagerConfig struct {
	StoreDSN        string
	ProfilingPolicy Policy
	TracingPolicy   Policy

	StatusPollInterval         time.Duration
	PendingTimeout             time.Duration
	CompletionGracePeriod      time.Duration
	NodeUnavailableGracePeriod time.Duration
	JobRetentionPeriod         time.Duration
}

type managedJob struct {
	mu sync.Mutex

	id                string
	kind              Kind
	hostname          string
	job               *Job
	operationObserved bool
	wake              chan struct{}
	recovered         bool
	cancel            context.CancelFunc
}

type activeHostKey [2]string

func newActiveHostKey(hostname string, kind Kind) activeHostKey {
	return activeHostKey{hostname, string(kind)}
}

// Manager owns all active Job state transitions for one Apiserver process.
type Manager struct {
	mu sync.RWMutex

	active      map[string]*managedJob
	activeTotal map[Kind]int
	activeHosts map[activeHostKey]int
	accepting   bool

	store      Store
	nodeClient NodeClient
	config     *ManagerConfig
	now        func() time.Time

	cleanupCancel context.CancelFunc
	closeOnce     sync.Once
	closeDone     chan struct{}
	closeErr      error
	wg            sync.WaitGroup

	quotaRejections     atomic.Uint64
	persistenceFailures atomic.Uint64
	recoveredJobs       atomic.Uint64
}

// ActiveJobStat contains one active Job metric bucket.
type ActiveJobStat struct {
	Kind   Kind
	Status Status
	Count  int
}

// ManagerStats is a point-in-time Job Manager metrics snapshot.
type ManagerStats struct {
	Active              []ActiveJobStat
	QuotaRejections     uint64
	PersistenceFailures uint64
	RecoveredJobs       uint64
}

// NewManager initializes storage, migrates records, and recovers active Jobs.
func NewManager(
	ctx context.Context,
	nodeClient NodeClient,
	config *ManagerConfig,
) (*Manager, error) {
	if nodeClient == nil {
		return nil, errors.New("create job manager: Node client is required")
	}
	normalized, err := normalizeManagerConfig(config)
	if err != nil {
		return nil, err
	}
	store, err := newStore(ctx, normalized.StoreDSN)
	if err != nil {
		return nil, err
	}
	manager := newManagerWithStore(store, nodeClient, normalized)
	if err := manager.recover(ctx); err != nil {
		_ = store.Close(ctx)
		return nil, fmt.Errorf("recover jobs: %w", err)
	}
	manager.startCleanup()
	return manager, nil
}

func newManagerWithStore(
	store Store,
	nodeClient NodeClient,
	config *ManagerConfig,
) *Manager {
	return &Manager{
		active:      make(map[string]*managedJob),
		activeTotal: make(map[Kind]int),
		activeHosts: make(map[activeHostKey]int),
		accepting:   true,
		store:       store,
		nodeClient:  nodeClient,
		config:      config,
		now: func() time.Time {
			return time.Now().UTC()
		},
		closeDone: make(chan struct{}),
	}
}

func normalizeManagerConfig(config *ManagerConfig) (*ManagerConfig, error) {
	if config == nil {
		return nil, errors.New("create job manager: config is required")
	}
	normalized := *config
	for _, policyConfig := range []struct {
		kind   Kind
		policy Policy
	}{
		{kind: KindProfiling, policy: normalized.ProfilingPolicy},
		{kind: KindTracing, policy: normalized.TracingPolicy},
	} {
		kind, policy := policyConfig.kind, policyConfig.policy
		if policy.MaxJobsPerHost == 0 && policy.MaxTotalJobs == 0 {
			return nil, fmt.Errorf(
				"create job manager: policy for %s is required",
				kind,
			)
		}
		if policy.MaxJobsPerHost <= 0 || policy.MaxTotalJobs <= 0 {
			return nil, fmt.Errorf(
				"create job manager: %s quotas must be greater than zero",
				kind,
			)
		}
	}
	if normalized.StatusPollInterval == 0 {
		normalized.StatusPollInterval = defaultStatusPollInterval
	}
	if normalized.PendingTimeout == 0 {
		normalized.PendingTimeout = defaultPendingTimeout
	}
	if normalized.CompletionGracePeriod == 0 {
		normalized.CompletionGracePeriod = defaultCompletionGracePeriod
	}
	if normalized.NodeUnavailableGracePeriod == 0 {
		normalized.NodeUnavailableGracePeriod = defaultNodeUnavailableGracePeriod
	}
	if normalized.JobRetentionPeriod == 0 {
		normalized.JobRetentionPeriod = defaultJobRetentionPeriod
	}
	for name, value := range map[string]time.Duration{
		"status poll interval":          normalized.StatusPollInterval,
		"pending timeout":               normalized.PendingTimeout,
		"completion grace period":       normalized.CompletionGracePeriod,
		"Node unavailable grace period": normalized.NodeUnavailableGracePeriod,
		"Job retention period":          normalized.JobRetentionPeriod,
	} {
		if value <= 0 {
			return nil, fmt.Errorf("create job manager: %s must be positive", name)
		}
	}
	return &normalized, nil
}

// Create persists one independent Job and starts its supervisor.
func (m *Manager) Create(ctx context.Context, request *CreateRequest) (*Job, error) {
	if request == nil {
		return nil, errors.New("create job: request is required")
	}
	now := m.now()
	newJob := &Job{
		ID:          "id-" + uuid.NewString(),
		Kind:        request.Spec.kind(),
		UserID:      request.UserID,
		Hostname:    request.Hostname,
		Duration:    request.Duration,
		Scope:       request.Scope,
		ContainerID: request.ContainerID,
		Spec:        request.Spec,
		Status:      StatusPending,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := newJob.validate(); err != nil {
		return nil, fmt.Errorf("create job: %w", err)
	}
	supervisorCtx, cancel := context.WithCancel(context.Background())
	runtime := newManagedJob(newJob, false, cancel)

	m.mu.Lock()
	if !m.accepting {
		m.mu.Unlock()
		return nil, ErrShuttingDown
	}
	policy, ok := m.config.policy(newJob.Kind)
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("create job manager: unsupported Job kind %q", newJob.Kind)
	}
	if m.activeTotal[newJob.Kind] >= policy.MaxTotalJobs ||
		m.activeHosts[newActiveHostKey(newJob.Hostname, newJob.Kind)] >=
			policy.MaxJobsPerHost {
		m.quotaRejections.Add(1)
		m.mu.Unlock()
		return nil, fmt.Errorf("%w: %s Job capacity is exhausted", ErrQuotaExceeded, newJob.Kind)
	}
	m.registerLocked(runtime)
	// Register before persistence so Shutdown cannot close the Store under Create.
	m.wg.Add(1)
	m.mu.Unlock()

	if err := m.store.Create(ctx, newJob); err != nil {
		cancel()
		m.persistenceFailures.Add(1)
		m.mu.Lock()
		m.unregisterLocked(runtime)
		m.mu.Unlock()
		m.wg.Done()
		return nil, fmt.Errorf("%w: create job %q: %w", ErrPersistence, newJob.ID, err)
	}
	go m.runSupervisor(supervisorCtx, runtime)
	return cloneJob(newJob), nil
}

func (c *ManagerConfig) policy(kind Kind) (Policy, bool) {
	switch kind {
	case KindProfiling:
		return c.ProfilingPolicy, true
	case KindTracing:
		return c.TracingPolicy, true
	default:
		return Policy{}, false
	}
}

// Get returns one durable Job snapshot.
func (m *Manager) Get(ctx context.Context, jobID string) (*Job, error) {
	return m.store.Get(ctx, jobID)
}

// ListPage returns one durable page and whether another page is available.
func (m *Manager) ListPage(ctx context.Context, query *Query) (*Page, error) {
	if query == nil || query.Limit <= 0 || query.Limit > maxJobPageSize {
		return nil, fmt.Errorf(
			"%w: page limit must be between 1 and %d",
			ErrInvalidQuery,
			maxJobPageSize,
		)
	}
	pageQuery := *query
	pageQuery.Limit++
	items, err := m.store.List(ctx, &pageQuery)
	if err != nil {
		return nil, err
	}
	hasMore := len(items) > query.Limit
	if hasMore {
		items = items[:query.Limit]
	}
	return &Page{Items: items, HasMore: hasMore}, nil
}

// Stop persists a user stop intent before allowing any Node Stop request.
func (m *Manager) Stop(ctx context.Context, jobID string) (*Job, error) {
	runtime := m.activeRuntime(jobID)
	if runtime == nil {
		storedJob, err := m.store.Get(ctx, jobID)
		if err != nil {
			return nil, err
		}
		if isTerminal(storedJob.Status) {
			return nil, ErrJobTerminal
		}
		return nil, fmt.Errorf("%w: active Job %q is not supervised", ErrPersistence, jobID)
	}

	runtime.mu.Lock()
	current := runtime.job
	if isTerminal(current.Status) {
		runtime.mu.Unlock()
		return nil, ErrJobTerminal
	}
	if current.Status == StatusStopping {
		result := cloneJob(current)
		runtime.mu.Unlock()
		return result, nil
	}

	now := m.now()
	updated := cloneJob(current)
	if current.Status == StatusPending && current.StartAttemptedAt.IsZero() {
		updated.StopReason = StopReasonUser
		updated.StopRequestedAt = now
		setTerminal(updated, OutcomeStopped, nil, now)
	} else {
		setStopping(updated, StopReasonUser, now, m.config.CompletionGracePeriod)
	}
	runtime.mu.Unlock()
	if err := m.persistRuntime(ctx, runtime, current, updated); err != nil {
		if errors.Is(err, ErrConflict) {
			return nil, fmt.Errorf("%w: persist stop for Job %q: %w", ErrConflict, jobID, err)
		}
		return nil, fmt.Errorf("%w: persist stop for Job %q: %w", ErrPersistence, jobID, err)
	}
	wakeSupervisor(runtime)
	return cloneJob(updated), nil
}

// Ready verifies that the durable Job Store can answer queries.
func (m *Manager) Ready(ctx context.Context) error {
	if err := m.store.Ping(ctx); err != nil {
		return fmt.Errorf("Job Store readiness: %w", err)
	}
	return nil
}

// Stats returns active Job and error counters without storage or network calls.
func (m *Manager) Stats() ManagerStats {
	m.mu.RLock()
	runtimes := make([]*managedJob, 0, len(m.active))
	for _, runtime := range m.active {
		runtimes = append(runtimes, runtime)
	}
	m.mu.RUnlock()

	counts := make(map[[2]string]int)
	for _, runtime := range runtimes {
		runtime.mu.Lock()
		key := [2]string{string(runtime.job.Kind), string(runtime.job.Status)}
		counts[key]++
		runtime.mu.Unlock()
	}
	active := make([]ActiveJobStat, 0, len(counts))
	for key, count := range counts {
		active = append(active, ActiveJobStat{Kind: Kind(key[0]), Status: Status(key[1]), Count: count})
	}
	return ManagerStats{
		Active:              active,
		QuotaRejections:     m.quotaRejections.Load(),
		PersistenceFailures: m.persistenceFailures.Load(),
		RecoveredJobs:       m.recoveredJobs.Load(),
	}
}

// Shutdown stops local supervisors without stopping Node Operations.
func (m *Manager) Shutdown(ctx context.Context) error {
	shutdownCtx := contextOrBackground(ctx)
	m.closeOnce.Do(func() {
		m.mu.Lock()
		m.accepting = false
		if m.cleanupCancel != nil {
			m.cleanupCancel()
		}
		for _, runtime := range m.active {
			runtime.cancel()
		}
		m.mu.Unlock()

		go func() {
			m.wg.Wait()
			m.closeErr = m.store.Close(shutdownCtx)
			close(m.closeDone)
		}()
	})
	select {
	case <-m.closeDone:
		return m.closeErr
	case <-shutdownCtx.Done():
		return shutdownCtx.Err()
	}
}

func (m *Manager) recover(ctx context.Context) error {
	jobs, err := m.store.List(ctx, &Query{Statuses: []Status{
		StatusPending,
		StatusRunning,
		StatusStopping,
	}})
	if err != nil {
		return err
	}
	for _, storedJob := range jobs {
		if _, ok := m.config.policy(storedJob.Kind); !ok {
			return fmt.Errorf("Job %q has no policy for kind %q", storedJob.ID, storedJob.Kind)
		}
		supervisorCtx, cancel := context.WithCancel(context.Background())
		runtime := newManagedJob(storedJob, true, cancel)
		m.mu.Lock()
		m.registerLocked(runtime)
		m.wg.Add(1)
		m.mu.Unlock()
		go m.runSupervisor(supervisorCtx, runtime)
	}
	m.recoveredJobs.Add(uint64(len(jobs)))
	return nil
}

func (m *Manager) startCleanup() {
	cleanupCtx, cancel := context.WithCancel(context.Background())
	m.mu.Lock()
	m.cleanupCancel = cancel
	m.wg.Add(1)
	m.mu.Unlock()
	go func(ctx context.Context) {
		defer m.wg.Done()
		ticker := time.NewTicker(jobCleanupInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				endedBefore := m.now().Add(-m.config.JobRetentionPeriod)
				if _, err := m.store.DeleteTerminalBefore(
					ctx,
					endedBefore,
					jobCleanupBatchSize,
				); err != nil {
					log.WithError(err).Error("failed to clean up terminal Jobs")
				}
			case <-ctx.Done():
				return
			}
		}
	}(cleanupCtx)
}

func (m *Manager) registerLocked(runtime *managedJob) {
	m.active[runtime.id] = runtime
	m.activeTotal[runtime.kind]++
	m.activeHosts[newActiveHostKey(runtime.hostname, runtime.kind)]++
}

func (m *Manager) unregisterLocked(runtime *managedJob) {
	current, ok := m.active[runtime.id]
	if !ok || current != runtime {
		return
	}
	delete(m.active, runtime.id)
	m.activeTotal[runtime.kind]--
	m.activeHosts[newActiveHostKey(runtime.hostname, runtime.kind)]--
}

func (m *Manager) activeRuntime(jobID string) *managedJob {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active[jobID]
}

func (m *Manager) runSupervisor(ctx context.Context, runtime *managedJob) {
	defer m.wg.Done()
	defer func() {
		m.mu.Lock()
		m.unregisterLocked(runtime)
		m.mu.Unlock()
	}()

	if runtime.recovered {
		if !waitForSupervisor(ctx, runtime.wake, recoveredPollJitter(
			runtime.id,
			m.config.StatusPollInterval,
		)) {
			return
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		terminal, err := m.superviseOnce(ctx, runtime)
		if err != nil && ctx.Err() == nil {
			log.WithError(err).WithField("job_id", runtime.id).
				Error("failed to supervise Job")
		}
		if terminal {
			return
		}
		if !waitForSupervisor(ctx, runtime.wake, m.nextWake(runtime)) {
			return
		}
	}
}

func newManagedJob(
	job *Job,
	recovered bool,
	cancel context.CancelFunc,
) *managedJob {
	return &managedJob{
		id:        job.ID,
		kind:      job.Kind,
		hostname:  job.Hostname,
		job:       cloneJob(job),
		wake:      make(chan struct{}, 1),
		recovered: recovered,
		cancel:    cancel,
	}
}

func waitForSupervisor(ctx context.Context, wake <-chan struct{}, delay time.Duration) bool {
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-wake:
		return true
	case <-ctx.Done():
		return false
	}
}

func wakeSupervisor(runtime *managedJob) {
	select {
	case runtime.wake <- struct{}{}:
	default:
	}
}

func recoveredPollJitter(jobID string, interval time.Duration) time.Duration {
	if interval <= 0 {
		return 0
	}
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(jobID))
	return time.Duration(hasher.Sum64() % uint64(interval))
}

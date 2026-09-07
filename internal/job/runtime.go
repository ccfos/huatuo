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
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"

	nodeapi "huatuo-bamai/apis/v1/node"
	"huatuo-bamai/internal/log"
)

type runtimePolicy struct {
	statusPollInterval         time.Duration
	pendingTimeout             time.Duration
	completionGracePeriod      time.Duration
	nodeUnavailableGracePeriod time.Duration
}

type runtimeDependencies struct {
	store               Store
	nodeClient          NodeClient
	policy              runtimePolicy
	now                 func() time.Time
	persistenceFailures *atomic.Uint64
}

type runtime struct {
	mu sync.Mutex

	id             string
	kind           Kind
	hostname       string
	job            *Job
	transitionGate chan struct{}
	wakeCh         chan struct{}
	recovered      bool
	cancel         context.CancelFunc
	dependencies   *runtimeDependencies
}

func newRuntime(
	job *Job,
	recovered bool,
	cancel context.CancelFunc,
	dependencies *runtimeDependencies,
) *runtime {
	return &runtime{
		id:             job.ID,
		kind:           job.Kind,
		hostname:       job.Hostname,
		job:            cloneJob(job),
		transitionGate: make(chan struct{}, 1),
		wakeCh:         make(chan struct{}, 1),
		recovered:      recovered,
		cancel:         cancel,
		dependencies:   dependencies,
	}
}

func (r *runtime) run(ctx context.Context) {
	if r.recovered {
		if !r.wait(ctx, recoveryStartJitter(
			r.dependencies.policy.statusPollInterval,
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
		terminal, err := r.superviseOnce(ctx)
		if errors.Is(err, ErrConflict) {
			terminal, err = r.reloadFromStore(ctx)
		}
		if err != nil && ctx.Err() == nil {
			log.WithError(err).WithField("job_id", r.id).
				Error("failed to supervise Job")
		}
		if terminal {
			return
		}
		if !r.wait(ctx, r.nextSupervisionDelay(err)) {
			return
		}
	}
}

func (r *runtime) superviseOnce(ctx context.Context) (bool, error) {
	r.mu.Lock()
	if isTerminal(r.job.Status) {
		r.mu.Unlock()
		return true, nil
	}
	shouldStart := r.job.Status == StatusPending && r.job.PendingDeadline.IsZero()
	r.mu.Unlock()

	var (
		operation *nodeapi.Operation
		err       error
	)
	if shouldStart {
		operation, err = r.startOperation(ctx)
		if errors.Is(err, ErrPersistence) || errors.Is(err, ErrConflict) {
			return false, err
		}
	} else {
		snapshot := r.snapshot()
		operation, err = r.dependencies.nodeClient.GetOperation(ctx, snapshot.Hostname, snapshot.ID)
	}
	if err != nil {
		return r.reconcileJobWithError(ctx, err)
	}

	return r.reconcileOperation(ctx, operation)
}

func (r *runtime) reloadFromStore(ctx context.Context) (bool, error) {
	if err := r.acquireTransition(ctx); err != nil {
		return false, err
	}
	defer r.releaseTransition()

	stored, err := r.dependencies.store.Get(ctx, r.id)
	if err != nil {
		return false, fmt.Errorf("%w: reload Job %q: %w", ErrPersistence, r.id, err)
	}
	if stored.ID != r.id || stored.Kind != r.kind || stored.Hostname != r.hostname {
		return false, fmt.Errorf(
			"%w: reload Job %q: immutable identity changed",
			ErrPersistence,
			r.id,
		)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.job = stored
	return isTerminal(stored.Status), nil
}

func (r *runtime) saveTransition(
	ctx context.Context,
	updated *Job,
) error {
	persisted, err := r.dependencies.store.Save(ctx, updated)
	if err != nil {
		r.dependencies.persistenceFailures.Add(1)
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.job = persisted
	return nil
}

func (r *runtime) acquireTransition(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case r.transitionGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *runtime) releaseTransition() {
	<-r.transitionGate
}

func (r *runtime) nextSupervisionDelay(superviseErr error) time.Duration {
	if superviseErr != nil {
		return r.dependencies.policy.statusPollInterval
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	current := r.job
	if isTerminal(current.Status) {
		return 0
	}
	now := r.dependencies.now()
	var deadline time.Time
	switch {
	// Node unavailability takes precedence because business deadlines cannot be
	// acted on until Node communication recovers.
	case !current.NodeUnavailableDeadline.IsZero():
		deadline = current.NodeUnavailableDeadline
	case current.Status == StatusPending:
		deadline = current.PendingDeadline
	case current.Status == StatusRunning:
		deadline = current.ExecutionDeadline
	case current.Status == StatusStopping:
		deadline = current.StopDeadline
	}

	delay := r.dependencies.policy.statusPollInterval
	if !deadline.IsZero() {
		delay = min(delay, deadline.Sub(now))
	}
	return max(delay, 0)
}

func (r *runtime) wait(ctx context.Context, delay time.Duration) bool {
	if delay < 0 {
		delay = 0
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-r.wakeCh:
		return true
	case <-ctx.Done():
		return false
	}
}

func (r *runtime) wake() {
	select {
	case r.wakeCh <- struct{}{}:
	default:
	}
}

func recoveryStartJitter(maxDelay time.Duration) time.Duration {
	if maxDelay <= 0 {
		return 0
	}
	return rand.N(maxDelay) //nolint:gosec // Startup jitter does not require cryptographic randomness.
}

func setStopping(job *Job, reason StopReason, now time.Time, gracePeriod time.Duration) {
	job.Status = StatusStopping
	job.StopReason = reason
	job.StopDeadline = now.Add(gracePeriod)
	job.UpdatedAt = now
}

func setTerminal(
	job *Job,
	terminal *TerminalResult,
	now time.Time,
) {
	job.Status = StatusTerminal
	job.Terminal = terminal
	job.UpdatedAt = now
	if job.EndedAt.IsZero() {
		job.EndedAt = now
	}
}

func (r *runtime) snapshot() *Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneJob(r.job)
}

func (r *runtime) status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.job.Status
}

func (r *runtime) stop(ctx context.Context) (*Job, error) {
	if err := r.acquireTransition(ctx); err != nil {
		return nil, err
	}
	defer r.releaseTransition()

	r.mu.Lock()
	current := r.job
	if isTerminal(current.Status) {
		r.mu.Unlock()
		return nil, ErrJobTerminal
	}
	if current.Status == StatusStopping {
		result := cloneJob(current)
		r.mu.Unlock()
		return result, nil
	}

	now := r.dependencies.now()
	updated := cloneJob(current)
	if current.Status == StatusPending && current.PendingDeadline.IsZero() {
		updated.StopReason = StopReasonUser
		setTerminal(updated, &TerminalResult{Outcome: OutcomeStopped}, now)
	} else {
		setStopping(
			updated,
			StopReasonUser,
			now,
			r.dependencies.policy.completionGracePeriod,
		)
	}
	r.mu.Unlock()
	if err := r.saveTransition(ctx, updated); err != nil {
		if errors.Is(err, ErrConflict) {
			return nil, fmt.Errorf("%w: persist stop for Job %q: %w", ErrConflict, r.id, err)
		}
		return nil, fmt.Errorf("%w: persist stop for Job %q: %w", ErrPersistence, r.id, err)
	}
	r.wake()
	return cloneJob(updated), nil
}

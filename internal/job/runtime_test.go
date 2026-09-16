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
	"testing"
	"time"
)

func TestRuntimeStopReadsStateAfterCurrentTransition(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	pendingJob := testJob("job-1", StatusPending, now)
	manager := testManager(newMemoryStore(pendingJob), &stubNodeClient{})
	setManagerNow(manager, func() time.Time { return now.Add(time.Second) })
	runtime := testRuntime(manager, pendingJob)

	if err := runtime.acquireTransition(t.Context()); err != nil {
		t.Fatalf("acquireTransition() error = %v", err)
	}
	result := make(chan struct {
		job *Job
		err error
	}, 1)
	go func() {
		job, err := runtime.stop(t.Context())
		result <- struct {
			job *Job
			err error
		}{job: job, err: err}
	}()

	runtime.mu.Lock()
	current := runtime.job
	updated := cloneJob(current)
	updated.PendingDeadline = now.Add(time.Minute)
	runtime.mu.Unlock()
	if err := runtime.saveTransition(t.Context(), updated); err != nil {
		runtime.releaseTransition()
		t.Fatalf("saveTransition() error = %v", err)
	}
	runtime.releaseTransition()

	got := <-result
	if got.err != nil {
		t.Fatalf("stop() error = %v", got.err)
	}
	if got.job.Status != StatusStopping {
		t.Fatalf("stop() status = %q, want %q", got.job.Status, StatusStopping)
	}
}

func TestRuntimeStopHonorsCancellationWhileWaitingForTransition(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	pendingJob := testJob("job-1", StatusPending, now)
	runtime := testRuntime(
		testManager(newMemoryStore(pendingJob), &stubNodeClient{}),
		pendingJob,
	)
	if err := runtime.acquireTransition(t.Context()); err != nil {
		t.Fatalf("acquireTransition() error = %v", err)
	}
	defer runtime.releaseTransition()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := runtime.stop(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("stop() error = %v, want context.Canceled", err)
	}
}

func TestRuntimeNodeUnavailableDoesNotSpinOnBusinessDeadline(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	runningJob := testJob("job-1", StatusRunning, now.Add(-time.Minute))
	runningJob.StartedAt = now.Add(-time.Minute)
	runningJob.ExecutionDeadline = now.Add(-time.Second)
	runningJob.NodeUnavailableDeadline = now.Add(time.Minute)
	manager := testManager(newMemoryStore(runningJob), &stubNodeClient{})
	manager.runtimeDeps.policy.statusPollInterval = 5 * time.Second
	setManagerNow(manager, func() time.Time { return now })
	runtime := testRuntime(manager, runningJob)

	if got := runtime.nextSupervisionDelay(nil); got != 5*time.Second {
		t.Fatalf("nextSupervisionDelay() = %s, want 5s", got)
	}
}

func TestRuntimeNextSupervisionDelayUsesStatusDeadline(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		status Status
		set    func(*Job)
	}{
		{
			name:   "pending",
			status: StatusPending,
			set:    func(job *Job) { job.PendingDeadline = now.Add(time.Second) },
		},
		{
			name:   "running",
			status: StatusRunning,
			set: func(job *Job) {
				job.StartedAt = now
				job.ExecutionDeadline = now.Add(time.Second)
			},
		},
		{
			name:   "stopping",
			status: StatusStopping,
			set: func(job *Job) {
				job.StopReason = StopReasonUser
				job.StopDeadline = now.Add(time.Second)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := testJob("job-1", tt.status, now)
			tt.set(job)
			manager := testManager(newMemoryStore(job), &stubNodeClient{})
			manager.runtimeDeps.policy.statusPollInterval = 5 * time.Second
			setManagerNow(manager, func() time.Time { return now })
			runtime := testRuntime(manager, job)

			if got := runtime.nextSupervisionDelay(nil); got != time.Second {
				t.Fatalf("nextSupervisionDelay() = %s, want 1s", got)
			}
		})
	}
}

func TestRuntimeSupervisorErrorUsesPollInterval(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	runningJob := testJob("job-1", StatusRunning, now.Add(-time.Minute))
	runningJob.ExecutionDeadline = now.Add(-time.Second)
	manager := testManager(newMemoryStore(runningJob), &stubNodeClient{})
	manager.runtimeDeps.policy.statusPollInterval = 5 * time.Second
	setManagerNow(manager, func() time.Time { return now })
	runtime := testRuntime(manager, runningJob)

	got := runtime.nextSupervisionDelay(ErrPersistence)
	if got != 5*time.Second {
		t.Fatalf("nextSupervisionDelay() = %s, want 5s", got)
	}
}

func TestRecoveryStartJitter(t *testing.T) {
	const maxDelay = time.Second
	for range 100 {
		got := recoveryStartJitter(maxDelay)
		if got < 0 || got >= maxDelay {
			t.Fatalf("recoveryStartJitter() = %s, want [0, %s)", got, maxDelay)
		}
	}

	for _, maxDelay := range []time.Duration{0, -time.Second} {
		if got := recoveryStartJitter(maxDelay); got != 0 {
			t.Fatalf("recoveryStartJitter(%s) = %s, want 0", maxDelay, got)
		}
	}
}

func TestRuntimeReloadFromStoreUsesPersistedState(t *testing.T) {
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	staleJob := testJob("job-1", StatusRunning, now.Add(-time.Minute))
	persistedJob := cloneJob(staleJob)
	setTerminal(persistedJob, &TerminalResult{Outcome: OutcomeCompleted}, now)
	manager := testManager(newMemoryStore(persistedJob), &stubNodeClient{})
	runtime := testRuntime(manager, staleJob)

	terminal, err := runtime.reloadFromStore(t.Context())
	if err != nil || !terminal {
		t.Fatalf("reloadFromStore() = (%t, %v), want (true, nil)", terminal, err)
	}
	got := runtime.snapshot()
	if got.Status != StatusTerminal || got.Terminal == nil ||
		got.Terminal.Outcome != OutcomeCompleted {
		t.Fatalf("reloaded Job = (%q, %+v), want succeeded terminal Job", got.Status, got.Terminal)
	}
}

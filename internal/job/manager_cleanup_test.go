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
	"testing"
	"time"
)

// limitingStore models the durable store's bounded delete: one call removes at
// most limit terminal Jobs that ended before the boundary. It records how often
// it was called and how many rows it removed.
type limitingStore struct {
	*memoryStore

	calls   int
	deleted int
	// failAt makes the n-th call (1-based) fail when non-zero.
	failAt int
	// onCall runs after each successful call.
	onCall func()
}

func (s *limitingStore) DeleteTerminalBefore(
	_ context.Context,
	endedBefore time.Time,
	limit int,
) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.calls++
	if s.failAt != 0 && s.calls == s.failAt {
		return 0, errors.New("store unavailable")
	}

	var removed int64
	for id, storedJob := range s.jobs {
		if removed == int64(limit) {
			break
		}
		if storedJob.Status != StatusTerminal || storedJob.EndedAt.IsZero() {
			continue
		}
		if !storedJob.EndedAt.Before(endedBefore) {
			continue
		}
		delete(s.jobs, id)
		removed++
		s.deleted++
	}

	if s.onCall != nil {
		s.onCall()
	}

	return removed, nil
}

func (s *limitingStore) stored() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.jobs)
}

func newCleanupManager(store Store, now time.Time, retention time.Duration) *Manager {
	return &Manager{
		config:      &ManagerConfig{JobRetentionPeriod: retention},
		store:       store,
		runtimeDeps: runtimeDependencies{now: func() time.Time { return now }},
	}
}

// seedExpiredTerminalJobs stores count terminal Jobs that all ended three days
// before the retention boundary.
func seedExpiredTerminalJobs(t *testing.T, store Store, count int, now time.Time, retention time.Duration) {
	t.Helper()

	ended := now.Add(-retention - 72*time.Hour)
	for i := 0; i < count; i++ {
		job := storedTestJob(
			fmt.Sprintf("expired-%04d", i),
			"user-1",
			"node-1",
			StatusTerminal,
			ended.Add(-time.Hour),
		)
		job.EndedAt = ended
		if err := store.Create(t.Context(), job); err != nil {
			t.Fatalf("Create(%d): %v", i, err)
		}
	}
}

// TestCleanupTerminalJobsDrainsBacklogOlderThanOneBatch pins that a backlog
// larger than a single delete batch is drained by one cleanup round. Cleaning up
// one batch per tick instead would let Jobs past the retention period stay
// stored whenever terminal Jobs are created faster than a batch per tick.
func TestCleanupTerminalJobsDrainsBacklogOlderThanOneBatch(t *testing.T) {
	const backlog = 2500

	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	retention := 30 * 24 * time.Hour
	store := &limitingStore{memoryStore: newMemoryStore()}
	seedExpiredTerminalJobs(t, store, backlog, now, retention)

	newCleanupManager(store, now, retention).cleanupTerminalJobs(t.Context())

	if want := backlog; store.deleted != want {
		t.Fatalf("deleted %d terminal Jobs, want %d", store.deleted, want)
	}
	if got := store.stored(); got != 0 {
		t.Fatalf("%d Jobs are still stored after the cleanup, want 0", got)
	}
	// 1000 + 1000 + 500, the short batch ends the loop.
	if want := backlog/jobCleanupBatchSize + 1; store.calls != want {
		t.Fatalf("DeleteTerminalBefore calls = %d, want %d", store.calls, want)
	}
}

func TestCleanupTerminalJobsKeepsJobsInsideRetention(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	retention := 30 * 24 * time.Hour
	store := &limitingStore{memoryStore: newMemoryStore()}
	seedExpiredTerminalJobs(t, store, 2, now, retention)

	recent := storedTestJob("recent", "user-1", "node-1", StatusTerminal, now.Add(-time.Hour))
	pending := storedTestJob("pending", "user-1", "node-1", StatusPending, now.Add(-48*time.Hour))
	for _, job := range []*Job{recent, pending} {
		if err := store.Create(t.Context(), job); err != nil {
			t.Fatalf("Create(%q): %v", job.ID, err)
		}
	}

	newCleanupManager(store, now, retention).cleanupTerminalJobs(t.Context())

	for _, id := range []string{"recent", "pending"} {
		if _, err := store.Get(t.Context(), id); err != nil {
			t.Fatalf("Job %q was deleted: %v", id, err)
		}
	}
	if got := store.stored(); got != 2 {
		t.Fatalf("%d Jobs are still stored, want the 2 recent ones", got)
	}
}

func TestCleanupTerminalJobsStopsOnStoreError(t *testing.T) {
	const backlog = 2500

	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	retention := 30 * 24 * time.Hour
	store := &limitingStore{memoryStore: newMemoryStore(), failAt: 2}
	seedExpiredTerminalJobs(t, store, backlog, now, retention)

	newCleanupManager(store, now, retention).cleanupTerminalJobs(t.Context())

	if store.calls != 2 {
		t.Fatalf("DeleteTerminalBefore calls = %d, want 2 (the loop must stop at the failing call)", store.calls)
	}
	if want := backlog - jobCleanupBatchSize; store.stored() != want {
		t.Fatalf("%d Jobs are still stored, want %d", store.stored(), want)
	}
}

func TestCleanupTerminalJobsStopsWhenContextIsCanceled(t *testing.T) {
	const backlog = 2500

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	retention := 30 * 24 * time.Hour
	store := &limitingStore{memoryStore: newMemoryStore(), onCall: cancel}
	seedExpiredTerminalJobs(t, store, backlog, now, retention)

	newCleanupManager(store, now, retention).cleanupTerminalJobs(ctx)

	if store.calls != 1 {
		t.Fatalf("DeleteTerminalBefore calls = %d, want 1 (the loop must stop once ctx is done)", store.calls)
	}
}

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

package autotracing

import (
	"context"
	"os"

	"github.com/ccfos/huatuo/internal/cgroups/memorywatch"
)

const (
	defaultMaxWatchedCgroups     = 4096
	defaultMemoryWatchEventBatch = 64

	memoryThresholdObserved = memorywatch.ThresholdObserved
	memoryTargetRemoved     = memorywatch.TargetRemoved
	memoryTargetUnavailable = memorywatch.TargetUnavailable
)

var (
	errMemoryWatchClosed           = memorywatch.ErrClosed
	errMemoryWatchLimit            = memorywatch.ErrTargetLimit
	errMemoryWatchEventOverflow    = memorywatch.ErrEventOverflow
	errMemoryWatchLimitUnavailable = memorywatch.ErrLimitUnavailable
)

type memoryWatchOptions struct {
	ThresholdPercent int
	MaxCgroups       int
}

// Reuse the native event contract without copying each notification batch.
// Registration IDs are never reused within one watcher lifetime.
type (
	memoryWatchRegistrationID = memorywatch.TargetID
	memoryWatchEvent          = memorywatch.Event
)

// memoryThresholdWatcher adapts kernel notifications for one scheduling owner.
// It accepts hierarchy-relative paths; target discovery belongs to the caller.
type memoryThresholdWatcher struct {
	watcher *memorywatch.Watcher
}

func newMemoryThresholdWatcher(ctx context.Context, opts memoryWatchOptions) (*memoryThresholdWatcher, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if opts.MaxCgroups == 0 {
		opts.MaxCgroups = defaultMaxWatchedCgroups
	}
	native, err := memorywatch.New(memorywatch.Options{
		ThresholdPercent: opts.ThresholdPercent, MaxCgroups: opts.MaxCgroups,
	})
	if err != nil {
		return nil, err
	}
	return &memoryThresholdWatcher{watcher: native}, nil
}

// Register is idempotent for the same live directory identity.
// The native watcher owns identity checks, rollback and the registration budget.
func (w *memoryThresholdWatcher) Register(ctx context.Context, path string, expectedIdentity os.FileInfo) (memoryWatchRegistrationID, error) {
	return w.watcher.Add(ctx, path, expectedIdentity)
}

// Unregister discards unread events for the ID. Already borrowed events remain
// valid values, so the target owner must reject retired registrations.
func (w *memoryThresholdWatcher) Unregister(ctx context.Context, id memoryWatchRegistrationID) error {
	return w.watcher.Remove(ctx, id)
}

// Notifications exposes coalesced kernel readiness, including terminal closure.
// The caller must ProcessEvents after a wakeup to obtain events or the failure.
func (w *memoryThresholdWatcher) Notifications() <-chan struct{} {
	return w.watcher.Notify()
}

// ProcessEvents never waits for new events and returns at most 64 observations.
// The batch is read-only and borrowed until the next call. Register, Unregister
// and Close do not modify it. Only one consumer may drain and process batches.
func (w *memoryThresholdWatcher) ProcessEvents(ctx context.Context) ([]memoryWatchEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-w.watcher.Notify():
	default:
	}
	return w.watcher.DrainEvents()
}

// Close rejects new operations and waits for native FD cleanup. It is idempotent.
func (w *memoryThresholdWatcher) Close() error {
	return w.watcher.Close()
}

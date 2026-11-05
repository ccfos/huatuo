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

package pod

import (
	"context"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/log"
)

const (
	workloadRefreshDelay   = 25 * time.Millisecond
	workloadResyncInterval = 30 * time.Second
)

// workloadManager owns the single producer loop for Host and Guest discovery.
// Discovery callbacks perform one synchronous attempt and never schedule work.
type workloadManager struct {
	store          *containerStore
	discover       func(context.Context, map[string]bool, bool) (containerSnapshot, error, bool)
	initialize     func(context.Context) error
	resyncInterval time.Duration
	wake           chan struct{}
	mu             sync.Mutex
	hints          map[string]bool
	full           bool
	cancel         context.CancelFunc
	done           chan struct{}
}

var (
	workloadManagerMu sync.Mutex
	activeManager     *workloadManager
)

func newWorkloadManager(store *containerStore) *workloadManager {
	return &workloadManager{
		store: store, resyncInterval: workloadResyncInterval,
		wake: make(chan struct{}, 1), hints: make(map[string]bool), full: true,
		done: make(chan struct{}),
	}
}

// ReleaseManager cancels I/O, joins the producer, and then releases CSS probes.
// Manager lifecycle calls are serialized by the daemon.
func ReleaseManager() {
	workloadManagerMu.Lock()
	controller := activeManager
	activeManager = nil
	workloadManagerMu.Unlock()
	if controller == nil {
		return
	}
	controller.cancel()
	<-controller.done
	kubeletPodListRunningEnabled.Store(false)
	releaseCgroupLifecycle()
}

func (c *workloadManager) start() {
	c.store.mu.Lock()
	c.store.isActive, c.store.err = true, ErrContainersUnavailable
	c.store.hasSnapshot = false
	c.store.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	go c.run(ctx)
}

// CSS events are hints, not confirmed container creates/deletes. Overflow
// requests a complete reconciliation and never blocks the perf reader.
func requestWorkloadRefresh(id string, removed bool) {
	workloadManagerMu.Lock()
	defer workloadManagerMu.Unlock()
	if activeManager != nil {
		activeManager.request(id, removed)
	}
}

func (c *workloadManager) request(id string, removed bool) {
	c.mu.Lock()
	if id == "" || len(c.hints) >= containerEventQueueSize {
		c.full = true
	} else {
		c.hints[id] = removed
	}
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *workloadManager) run(ctx context.Context) {
	defer close(c.done)
	defer func() {
		c.store.mu.Lock()
		defer c.store.mu.Unlock()
		c.store.isActive, c.store.err = false, ErrContainerManagerDisabled
		for s := range c.store.subscribers {
			s.closeLocked()
		}
		clear(c.store.records)
		clear(c.store.cgroupOwners)
		clear(c.store.cssOwners)
	}()
	timer := time.NewTimer(0)
	defer timer.Stop()
	delay := time.Second
	due := time.Now()
	initialized := c.initialize == nil
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
			// Leave the timer unchanged on duplicate hints; bursts must not
			// postpone synchronization forever.
			if time.Until(due) > workloadRefreshDelay {
				due = time.Now().Add(workloadRefreshDelay)
				timer.Reset(workloadRefreshDelay)
			}
		case <-timer.C:
			if !initialized {
				if err := c.initialize(ctx); err != nil {
					c.store.commit(containerSnapshot{}, err)
					log.WithError(err).Warn("initialize workload discovery")
					due = time.Now().Add(delay)
					timer.Reset(delay)
					delay = min(2*delay, c.resyncInterval)
					continue
				}
				initialized = true
			}
			c.mu.Lock()
			hints, full := c.hints, c.full
			c.hints, c.full = make(map[string]bool), false
			c.mu.Unlock()
			snapshot, err, retry := c.discover(ctx, hints, full)
			if ctx.Err() != nil {
				return
			}
			c.store.commit(snapshot, err)
			if err != nil {
				log.WithError(err).Warn("refresh workload view")
			}
			// Kernel hints can precede source state or be lost entirely.
			// Keep reconciling membership after readiness retries are exhausted.
			next := c.resyncInterval
			if retry {
				next = min(delay, c.resyncInterval)
				delay = min(2*delay, c.resyncInterval)
			} else {
				delay = time.Second
			}
			due = time.Now().Add(next)
			timer.Reset(next)
		}
	}
}

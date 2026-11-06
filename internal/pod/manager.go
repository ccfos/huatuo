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
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/procfs"
)

const (
	workloadRefreshDelay     = 25 * time.Millisecond
	workloadResyncInterval   = 30 * time.Second
	workloadDiscoveryTimeout = 10 * time.Second
)

// workloadManager owns the single producer loop for Host and Guest discovery.
// Discovery callbacks perform one synchronous attempt and never schedule work.
type workloadManager struct {
	store            *containerStore
	discover         func(context.Context, map[string]bool, bool) (containerSnapshot, error, bool)
	initialize       func(context.Context) error
	resyncInterval   time.Duration
	discoveryTimeout time.Duration
	wake             chan struct{}
	mu               sync.Mutex
	hints            map[string]bool
	full             bool
	cancel           context.CancelFunc
	done             chan struct{}
}

var (
	workloadManagerMu sync.Mutex
	activeManager     *workloadManager
)

func newWorkloadManager(store *containerStore) *workloadManager {
	return &workloadManager{
		store: store, resyncInterval: workloadResyncInterval, discoveryTimeout: workloadDiscoveryTimeout,
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
				attemptCtx, cancel := context.WithTimeout(ctx, c.discoveryTimeout)
				err := c.initialize(attemptCtx)
				if err == nil {
					err = attemptCtx.Err()
				}
				cancel()
				if ctx.Err() != nil {
					return
				}
				if err != nil {
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
			attemptCtx, cancel := context.WithTimeout(ctx, c.discoveryTimeout)
			snapshot, err, retry := c.discover(attemptCtx, hints, full)
			if timeoutErr := attemptCtx.Err(); timeoutErr != nil {
				// A timed-out result cannot authorize deletion or advance sync time.
				snapshot, err, retry = containerSnapshot{}, timeoutErr, true
				// The source may have consumed hints before its result was dropped.
				c.mu.Lock()
				c.full = true
				c.mu.Unlock()
			}
			cancel()
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

type ManagerOptions struct {
	SyncInterval     time.Duration
	DiscoveryTimeout time.Duration
}

// StartManager selects exactly one source. Zero durations use the defaults.
func StartManager(source Source, options ManagerOptions) error {
	manager, err := newSourceManager(containerView, source, options)
	if err != nil {
		return err
	}
	workloadManagerMu.Lock()
	defer workloadManagerMu.Unlock()
	if activeManager != nil {
		return errors.New("workload manager is already running")
	}
	manager.initialize = func(context.Context) error {
		// Missing CSS must not disable metadata and basic cgroup file collection.
		if err := initCgroupLifecycle(); err != nil {
			log.WithError(err).Warn("cgroup CSS unavailable; workload file collection remains enabled")
		}
		return nil
	}
	activeManager = manager
	manager.start()
	return nil
}

func newSourceManager(store *containerStore, source Source, options ManagerOptions) (*workloadManager, error) {
	if source == nil {
		return nil, errors.New("workload discovery source is required")
	}
	kind := source.Type()
	switch kind {
	case TypeContainer, TypeCubeSandbox, TypeOpenSandbox:
	default:
		return nil, fmt.Errorf("unsupported workload source type %q", kind)
	}
	if options.SyncInterval < 0 || options.DiscoveryTimeout < 0 {
		return nil, errors.New("workload sync interval and discovery timeout must not be negative")
	}
	manager := newWorkloadManager(store)
	if options.SyncInterval > 0 {
		manager.resyncInterval = options.SyncInterval
	}
	if options.DiscoveryTimeout > 0 {
		manager.discoveryTimeout = options.DiscoveryTimeout
	}
	manager.discover = func(ctx context.Context, _ map[string]bool, _ bool) (containerSnapshot, error, bool) {
		discovery, sourceErr := source.Discover(ctx)
		if err := ctx.Err(); err != nil {
			return containerSnapshot{}, err, true
		}
		snapshot, err := manager.bindDiscovery(ctx, kind, discovery)
		err = errors.Join(sourceErr, err)
		return snapshot, err, err != nil
	}
	return manager, nil
}

func (m *workloadManager) bindDiscovery(ctx context.Context, kind string, discovery Discovery) (containerSnapshot, error) {
	snapshot := containerSnapshot{records: make(map[string]*containerRecord, len(discovery.Pods)), isComplete: discovery.Complete}
	mounts, _ := cgroupMounts()
	var resultErr error
	for id, discovered := range discovery.Pods {
		if err := ctx.Err(); err != nil {
			return containerSnapshot{}, err
		}
		if id == "" || (discovered != nil && (discovered.ID != id || discovered.Type != kind)) {
			snapshot.isComplete = false
			resultErr = errors.Join(resultErr, fmt.Errorf("source %q returned mismatched workload identity for key %q", kind, id))
			if id != "" {
				snapshot.records[id] = nil
			}
			continue
		}
		if discovered == nil {
			snapshot.records[id] = nil
			continue
		}
		workload := discovered.copyMetadata()
		workload.lifeResources = nil
		workload.SyncedAt = time.Time{}
		locations := CgroupBinding{Unified: workload.Cgroups.Unified, Controllers: workload.Cgroups.Controllers}
		workload.Cgroups = bindCgroups(locations, mounts)
		bindHostResources(workload, procfs.DefaultPath())
		record := &containerRecord{pod: workload, cgroupLocations: locations}
		if kind == TypeContainer {
			record.container = containerFromWorkload(discovered, workload)
			if discovered.Process != nil {
				record.ref.InitPID = discovered.Process.PID
			}
			if workload.Process != nil {
				record.startTime = workload.Process.startTime
			}
			isHostKata := workload.Container != nil && isKataRuntime(workload.Container.RuntimeType)
			if membership, ok := workload.Cgroups.Path("memory"); ok && !isHostKata {
				record.ref.MemoryCgroupPath = membership
				if directory, err := workload.Cgroups.Directory("memory"); err == nil {
					record.directory, _ = os.Lstat(directory)
				}
			}
			// Missing resource readiness is not proof of a new container instance.
			m.store.mu.RLock()
			if previous := m.store.records[id]; previous != nil && previous.pod.Type == TypeContainer {
				if record.ref.InitPID == 0 {
					record.ref.InitPID = previous.ref.InitPID
				}
				if record.startTime == 0 && record.ref.InitPID == previous.ref.InitPID {
					record.startTime = previous.startTime
				}
				if record.ref.MemoryCgroupPath == "" {
					record.ref.MemoryCgroupPath = previous.ref.MemoryCgroupPath
				}
				if record.directory == nil && record.ref.MemoryCgroupPath == previous.ref.MemoryCgroupPath {
					record.directory = previous.directory
				}
			}
			m.store.mu.RUnlock()
		}
		if peer := discovery.GuestPeers[id]; peer.PID > 0 {
			record.guestPeer = peer
		}
		snapshot.records[id] = record
	}
	return snapshot, resultErr
}

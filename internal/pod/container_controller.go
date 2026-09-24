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
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/ccfos/huatuo/internal/log"
)

const containerRefreshDelay = 25 * time.Millisecond

type containerController struct {
	store             *containerStore
	fetch             func(context.Context) (corev1.PodList, error)
	resolve           func(string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*containerRecord, error)
	initialize        func(context.Context) error
	wake              chan struct{}
	mu                sync.Mutex
	hints             map[string]bool
	full              bool
	retries           map[string]*containerRefreshRetry
	needsFull         bool
	malformedAttempts int
	cancel            context.CancelFunc
	done              chan struct{}
}

type containerRefreshRetry struct {
	attempts         int
	awaitingCreation bool
	err              error
}

var (
	containerManagerMu sync.Mutex
	containerManager   *containerController
)

// InitManager starts one shared producer before any initial container lookup.
// Transient startup failures are visible to subscribers and retried by the
// controller; getters never perform network I/O or start background work.
func InitManager(config *ManagerCtx) error {
	containerManagerMu.Lock()
	defer containerManagerMu.Unlock()
	if containerManager != nil {
		return nil
	}
	if config.PodReadOnlyPort == 0 && config.PodAuthorizedPort == 0 {
		return nil
	}
	if config.PodReadOnlyPort == 0 && config.PodClientCertPath == "" {
		return errors.New("authorized kubelet port requires a client certificate")
	}
	managerConfig := *config
	certs := strings.Split(config.PodClientCertPath, ",")
	managerConfig.podClientCertPath = strings.TrimSpace(certs[0])
	managerConfig.podClientCertKey = managerConfig.podClientCertPath
	if len(certs) > 1 {
		managerConfig.podClientCertKey = strings.TrimSpace(certs[1])
	}
	dockerAPIVersion = config.DockerAPIVersion
	controller := newContainerController(containerView)
	controller.initialize = func(ctx context.Context) error {
		if err := kubeletPodListPortCacheUpdate(ctx, &managerConfig); err != nil {
			return err
		}
		if err := kubeletConfigCacheMustUpdate(&managerConfig); err != nil {
			return err
		}
		return containerCgroupCssInit()
	}
	containerManager = controller
	controller.start()
	return nil
}

// ReleaseManager cancels I/O, joins the producer, and then releases CSS probes.
// Manager lifecycle calls are serialized by the daemon.
func ReleaseManager() {
	containerManagerMu.Lock()
	controller := containerManager
	containerManager = nil
	containerManagerMu.Unlock()
	if controller == nil {
		return
	}
	controller.cancel()
	<-controller.done
	containerCgroupCssRelease()
}

func newContainerController(store *containerStore) *containerController {
	return &containerController{
		store: store, fetch: kubeletGetPodList, resolve: resolveContainerRecord,
		wake: make(chan struct{}, 1), hints: make(map[string]bool), full: true,
		done: make(chan struct{}), retries: make(map[string]*containerRefreshRetry),
	}
}

func (c *containerController) start() {
	c.store.mu.Lock()
	c.store.isActive, c.store.err = true, ErrContainersUnavailable
	c.store.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	go c.run(ctx)
}

// CSS events are hints, not confirmed container creates/deletes. Overflow
// requests a complete reconciliation and never blocks the perf reader.
func requestContainerRefresh(id string, removed bool) {
	containerManagerMu.Lock()
	defer containerManagerMu.Unlock()
	if containerManager != nil {
		containerManager.request(id, removed)
	}
}

func (c *containerController) request(id string, removed bool) {
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

func (c *containerController) run(ctx context.Context) {
	defer close(c.done)
	defer func() {
		c.store.mu.Lock()
		defer c.store.mu.Unlock()
		c.store.isActive, c.store.err = false, ErrContainerManagerDisabled
		for s := range c.store.subscribers {
			s.closeLocked()
		}
		clear(c.store.records)
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
			if due.IsZero() || time.Until(due) > containerRefreshDelay {
				due = time.Now().Add(containerRefreshDelay)
				timer.Reset(containerRefreshDelay)
			}
		case <-timer.C:
			due = time.Time{}
			if !initialized {
				if err := c.initialize(ctx); err != nil {
					c.store.commit(nil, err)
					log.WithError(err).Warn("initialize container state producer")
					due = time.Now().Add(delay)
					timer.Reset(delay)
					delay = min(2*delay, 30*time.Second)
					continue
				}
				initialized = true
			}
			c.mu.Lock()
			hints, full := c.hints, c.full
			c.hints, c.full = make(map[string]bool), false
			c.mu.Unlock()
			records, err, retry := c.refresh(ctx, hints, full)
			if ctx.Err() != nil {
				return
			}
			c.store.commit(records, err)
			if err != nil {
				log.WithError(err).Warn("refresh container view")
				if retry {
					due = time.Now().Add(delay)
					timer.Reset(delay)
					delay = min(2*delay, 30*time.Second)
				}
			} else {
				delay = time.Second
			}
		}
	}
}

// refresh retries unresolved instances without re-resolving healthy records.
// Upstream query failures retry with capped backoff; individual readiness
// failures stop after three attempts and remain visible until another hint.
func (c *containerController) refresh(ctx context.Context, hints map[string]bool, full bool) (map[string]*containerRecord, error, bool) {
	if full {
		clear(c.retries)
		c.needsFull = true
	}
	if full || len(hints) != 0 {
		c.malformedAttempts = 0
	}
	for id, removed := range hints {
		c.retries[id] = &containerRefreshRetry{awaitingCreation: !removed}
	}
	list, err := c.fetch(ctx)
	if err != nil {
		return nil, err, true
	}
	records := make(map[string]*containerRecord)
	seen := make(map[string]struct{})
	var syncErr error
	retry := false
	malformed := false
	for i := range list.Items {
		if err := ctx.Err(); err != nil {
			return nil, err, false
		}
		p := &list.Items[i]
		// Completed init and ephemeral debug containers are outside the view.
		// Restartable init sidecars are included even before the Pod is Running.
		for _, group := range []struct {
			specs    []corev1.Container
			statuses []corev1.ContainerStatus
			init     bool
		}{
			{p.Spec.Containers, p.Status.ContainerStatuses, false},
			{p.Spec.InitContainers, p.Status.InitContainerStatuses, true},
		} {
			for j := range group.statuses {
				status := &group.statuses[j]
				if status.State.Running == nil {
					continue
				}
				var spec *corev1.Container
				for k := range group.specs {
					if group.specs[k].Name == status.Name {
						spec = &group.specs[k]
						break
					}
				}
				if spec == nil {
					syncErr = fmt.Errorf("running container %q has no pod spec", status.Name)
					malformed = true
					continue
				}
				if group.init && (spec.RestartPolicy == nil || *spec.RestartPolicy != corev1.ContainerRestartPolicyAlways) {
					continue
				}
				id, err := parseContainerIDInPodStatus(status.ContainerID)
				if err != nil {
					syncErr = err
					malformed = true
					continue
				}
				seen[id] = struct{}{}
				c.store.mu.RLock()
				previous := c.store.records[id]
				c.store.mu.RUnlock()
				pending := c.retries[id]
				if pending == nil && previous != nil && !c.needsFull && previous.container.StartedAt.Equal(status.State.Running.StartedAt.Time) {
					records[id] = previous
					continue
				}
				if pending == nil {
					pending = &containerRefreshRetry{}
					c.retries[id] = pending
				}
				pending.awaitingCreation = false
				if pending.attempts >= 3 {
					if previous != nil {
						records[id] = previous
					}
					syncErr = pending.err
					continue
				}
				record, err := c.resolve(id, spec, status, p)
				if err != nil {
					pending.attempts++
					pending.err = fmt.Errorf("resolve container %q: %w", id, err)
					syncErr = pending.err
					retry = retry || pending.attempts < 3
					if previous != nil {
						records[id] = previous
					} else if record != nil {
						records[id] = record
					}
					continue
				}
				records[id] = record
				delete(c.retries, id)
			}
		}
	}
	c.needsFull = false
	for id, pending := range c.retries {
		if _, exists := seen[id]; exists {
			continue
		}
		if !pending.awaitingCreation {
			delete(c.retries, id)
			continue
		}
		pending.attempts++
		pending.err = fmt.Errorf("container %q creation is not yet reflected by kubelet", id)
		syncErr = pending.err
		retry = retry || pending.attempts < 3
	}
	if malformed {
		c.malformedAttempts++
		retry = retry || c.malformedAttempts < 3
	}
	return records, syncErr, retry
}

func resolveContainerRecord(id string, spec *corev1.Container, status *corev1.ContainerStatus, p *corev1.Pod) (*containerRecord, error) {
	container, err := kubeletContainer(id, spec, status, p)
	if err != nil {
		return nil, err
	}
	record, err := containerMemoryBinding(container)
	if err != nil {
		return &containerRecord{container: container, ref: ContainerRef{Key: ContainerKey{ID: id}, InitPID: container.InitPid}}, err
	}
	return record, nil
}

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

	corev1 "k8s.io/api/core/v1"
)

type containerController struct {
	*workloadManager
	fetch             func(context.Context) (corev1.PodList, error)
	resolve           func(string, *corev1.Container, *corev1.ContainerStatus, *corev1.Pod) (*containerRecord, error)
	retries           map[string]*containerRefreshRetry
	needsFull         bool
	malformedAttempts int
}

type containerRefreshRetry struct {
	attempts         int
	awaitingCreation bool
	awaitingRemoval  bool
	err              error
}

// InitManager starts one shared producer before any initial container lookup.
// Transient startup failures are visible to subscribers and retried by the
// controller; getters never perform network I/O or start background work.
func InitManager(config *ManagerCtx) error {
	workloadManagerMu.Lock()
	defer workloadManagerMu.Unlock()
	if activeManager != nil {
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
		return initCgroupLifecycle()
	}
	activeManager = controller.workloadManager
	controller.start()
	return nil
}

func newContainerController(store *containerStore) *containerController {
	controller := &containerController{
		workloadManager: newWorkloadManager(store),
		fetch:           kubeletGetPodList, resolve: resolveContainerRecord,
		retries: make(map[string]*containerRefreshRetry),
	}
	controller.discover = controller.refresh
	return controller
}

// refresh retries unresolved instances without re-resolving healthy non-Kata records.
// Upstream query failures retry with capped backoff; individual readiness
// failures for new instances stop after three attempts until another hint.
// Known Kata instances keep retrying because runtime outages are recoverable.
// Deletion hints keep retrying until kubelet confirms absence.
func (c *containerController) refresh(ctx context.Context, hints map[string]bool, full bool) (containerSnapshot, error, bool) {
	if full {
		clear(c.retries)
		c.needsFull = true
	}
	if full || len(hints) != 0 {
		c.malformedAttempts = 0
	}
	for id, removed := range hints {
		c.retries[id] = &containerRefreshRetry{awaitingCreation: !removed, awaitingRemoval: removed}
	}
	list, err := c.fetch(ctx)
	if err != nil {
		return containerSnapshot{}, err, true
	}
	records := make(map[string]*containerRecord)
	var syncErr error
	retry := false
	malformed := false
	for i := range list.Items {
		if err := ctx.Err(); err != nil {
			return containerSnapshot{}, err, false
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
				c.store.mu.RLock()
				previous := c.store.records[id]
				c.store.mu.RUnlock()
				// Retain membership even when metadata cannot be resolved yet.
				records[id] = previous
				pending := c.retries[id]
				removing := pending != nil && pending.awaitingRemoval
				retry = retry || removing
				if (pending == nil ||
					removing) &&
					previous != nil &&
					previous.container.HostKataSandboxID() == "" &&
					!c.needsFull &&
					previous.container.StartedAt.Equal(
						status.State.Running.StartedAt.Time,
					) {
					continue
				}
				if pending == nil {
					pending = &containerRefreshRetry{}
					c.retries[id] = pending
				}
				pending.awaitingCreation = false
				isKnownKata := previous != nil && previous.container.HostKataSandboxID() != ""
				if pending.attempts >= 3 && !isKnownKata {
					syncErr = pending.err
					continue
				}
				record, err := c.resolve(id, spec, status, p)
				if err != nil {
					if pending.attempts < 3 {
						pending.attempts++
					}
					pending.err = fmt.Errorf("resolve container %q: %w", id, err)
					syncErr = pending.err
					retry = retry || isKnownKata || pending.attempts < 3
					if previous == nil && record != nil {
						records[id] = record
					}
					continue
				}
				records[id] = record
				if !removing {
					delete(c.retries, id)
				}
			}
		}
	}
	c.needsFull = false
	for id, pending := range c.retries {
		if _, exists := records[id]; exists {
			continue
		}
		if !pending.awaitingCreation {
			delete(c.retries, id)
			continue
		}
		pending.attempts++
		// CSS also reports containers outside the kubelet view. A hint is
		// not evidence that the successfully fetched list is incomplete.
		if pending.attempts >= 3 {
			delete(c.retries, id)
			continue
		}
		pending.err = fmt.Errorf("container %q creation is not yet reflected by kubelet", id)
		syncErr = pending.err
		retry = retry || pending.attempts < 3
	}
	if malformed {
		c.malformedAttempts++
		retry = retry || c.malformedAttempts < 3
	}
	return containerSnapshot{records: records, isComplete: !malformed}, syncErr, retry
}

func resolveContainerRecord(id string, spec *corev1.Container, status *corev1.ContainerStatus, p *corev1.Pod) (*containerRecord, error) {
	container, err := kubeletContainer(id, spec, status, p)
	if err != nil {
		return nil, err
	}
	record, err := containerMemoryBinding(container)
	if err != nil {
		if record != nil {
			return record, err
		}
		workload := podFromContainer(container)
		workload.Cgroups = CgroupBinding{}
		workload.Process = nil
		workload.Network = nil
		return &containerRecord{
			container: container, pod: workload,
			ref: ContainerRef{Key: ContainerKey{ID: id}, InitPID: container.InitPid},
		}, err
	}
	return record, nil
}

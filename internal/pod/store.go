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
	"fmt"
	"maps"
	"os"
	"sync"
	"time"
)

type containerRecord struct {
	pod             *Pod
	cgroupLocations CgroupBinding
	// container is the read-only compatibility view of the same instance.
	container *Container
	ref       ContainerRef
	startTime uint64
	guest     *guestScanner
	directory os.FileInfo
}

func (r *containerRecord) sameInstance(other *containerRecord) bool {
	if r.pod != nil && other.pod != nil && r.pod.Type != other.pod.Type {
		return false
	}
	// Completing previously unknown identity is not evidence of a restart.
	return (r.ref.InitPID == 0 || r.ref.InitPID == other.ref.InitPID) &&
		(r.startTime == 0 || r.startTime == other.startTime) &&
		(r.ref.MemoryCgroupPath == "" || r.ref.MemoryCgroupPath == other.ref.MemoryCgroupPath) &&
		(r.directory == nil || (other.directory != nil && os.SameFile(r.directory, other.directory)))
}

// A complete snapshot accounts for every in-scope ID. A nil record retains
// membership while metadata is unresolved; incomplete snapshots cannot delete.
type containerSnapshot struct {
	records    map[string]*containerRecord
	isComplete bool
}

type containerStore struct {
	mu          sync.RWMutex
	records     map[string]*containerRecord
	subscribers map[*subscription]struct{}
	revision    uint64
	err         error
	isActive    bool
	hasSnapshot bool
}

func newContainerStore() *containerStore {
	return &containerStore{
		records:     make(map[string]*containerRecord),
		subscribers: make(map[*subscription]struct{}),
		err:         ErrContainerManagerDisabled,
	}
}

var containerView = newContainerStore()

func (r *containerRecord) prepareView(now time.Time) {
	if r.container != nil && (r.pod == nil || r.pod.Type == TypeContainer) {
		view := *r.container
		view.Labels = maps.Clone(view.Labels)
		view.CgroupCss = maps.Clone(view.CgroupCss)
		if view.memoryCgroupPath != nil {
			memory := *view.memoryCgroupPath
			view.memoryCgroupPath = &memory
		}
		r.container = &view
		if r.pod == nil {
			r.pod = podFromContainer(&view)
		} else {
			r.pod = r.pod.copyMetadata()
		}
	} else {
		r.pod = r.pod.copyMetadata()
		r.container = nil
	}
	r.pod.SyncedAt = now
}

func (r *containerRecord) setLifeResources(resources map[string]any) {
	r.pod.lifeResources = resources
	if r.container != nil {
		r.container.lifeResources = resources
	}
}

// commit keeps membership completeness separate from metadata readiness, so
// one unresolved container cannot retain unrelated deleted containers.
func (v *containerStore) commit(snapshot containerSnapshot, syncErr error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if !v.isActive {
		return
	}
	if snapshot.isComplete {
		for id, previous := range v.records {
			if _, exists := snapshot.records[id]; !exists {
				delete(v.records, id)
				v.publish(PodDeleted, previous)
			}
		}
	}
	now := time.Now()
	resourcesChanged := false
	for id, record := range snapshot.records {
		previous := v.records[id]
		if record == nil || (record.pod == nil && record.container == nil) {
			continue
		}
		if previous == record && record.pod != nil {
			// A successful discovery can reuse metadata without mutating readers'
			// published Pod or changing Kata's legacy representative timestamp.
			view := *record.pod
			view.SyncedAt = now
			updated := *record
			updated.pod = &view
			v.records[id] = &updated
			continue
		}
		record.prepareView(now)
		if previous != nil && previous.sameInstance(record) {
			if record.container != nil && previous.container != nil && (record.guest != nil ||
				(record.container.HostKataSandboxID() != "" &&
					record.container.HostKataSandboxID() == previous.container.HostKataSandboxID())) {
				record.container.StartedAt = previous.container.StartedAt
				record.container.SyncedAt = previous.container.SyncedAt
				record.pod.StartedAt = previous.pod.StartedAt
			}
			record.ref.Key = previous.ref.Key
			resourcesChanged = resourcesChanged || previous.ref != record.ref ||
				!previous.pod.sameResources(record.pod) || (previous.directory == nil && record.directory != nil) ||
				(previous.startTime == 0 && record.startTime != 0)
			record.setLifeResources(previous.pod.lifeResources)
			v.records[id] = record
			continue
		}
		if previous != nil {
			v.publish(PodDeleted, previous)
		}
		record.ref.Key = ContainerKey{ID: id, Generation: v.revision + 1}
		record.setLifeResources(newLifeResources())
		v.records[id] = record
		v.publish(PodCreated, record)
	}
	v.hasSnapshot = v.hasSnapshot || snapshot.isComplete || len(v.records) > 0
	wasUnavailable := v.err != nil
	v.err = nil
	if syncErr != nil {
		v.err = fmt.Errorf("%w: %w", ErrContainersUnavailable, syncErr)
	}
	if resourcesChanged {
		v.revision++
	}
	if resourcesChanged || wasUnavailable || syncErr != nil {
		for s := range v.subscribers {
			s.needsFull = true
			s.signal()
		}
	}
}

// ContainerRefByID reads the current instance without enumeration or runtime I/O.
// A zero reference means confirmed absence; an unavailable view returns an error.
func ContainerRefByID(id string) (ContainerRef, error) {
	containerView.mu.RLock()
	defer containerView.mu.RUnlock()
	if containerView.err != nil {
		return ContainerRef{}, containerView.err
	}
	if record := containerView.records[id]; record != nil && record.container != nil {
		return record.ref, nil
	}
	return ContainerRef{}, nil
}

// MemoryCgroupDirectory returns the published directory identity for ref.
// It never inspects the path again, which may now name another instance.
func MemoryCgroupDirectory(ref ContainerRef) (os.FileInfo, error) {
	return containerView.memoryCgroupDirectory(ref)
}

func (v *containerStore) memoryCgroupDirectory(ref ContainerRef) (os.FileInfo, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.err != nil {
		return nil, v.err
	}

	record := v.records[ref.Key.ID]
	if record == nil || record.ref != ref {
		return nil, fmt.Errorf("container %q instance is no longer current", ref.Key.ID)
	}
	if record.directory == nil || ref.MemoryCgroupPath == "" {
		return nil, fmt.Errorf("container %q memory cgroup binding is pending", ref.Key.ID)
	}

	return record.directory, nil
}

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
	"bufio"
	"fmt"
	"os"
	"sync"
	"sync/atomic"

	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/cgroups"
	"github.com/ccfos/huatuo/internal/procfs"
)

const cgroupChangeQueueSize = 256

// MemoryCgroupChange leaves path resolution to the subscriber.
type MemoryCgroupChange struct {
	ContainerID string
	Removed     bool
}

// MemoryCgroupSubscription shares the existing CSS lifecycle probes.
// The wake callback must be nonblocking; Close waits for callbacks to finish.
type MemoryCgroupSubscription struct {
	changes   chan MemoryCgroupChange
	wake      func()
	hierarchy int32
	resync    atomic.Bool
}

var (
	cgroupLifecycleMu         sync.Mutex
	cgroupLifecycleUsers      int
	cgroupLifecycleDone       <-chan struct{}
	memoryCgroupSubscribersMu sync.RWMutex
	memoryCgroupSubscribers   = make(map[*MemoryCgroupSubscription]struct{})
)

func acquireCgroupLifecycle() error {
	cgroupLifecycleMu.Lock()
	defer cgroupLifecycleMu.Unlock()
	if cgroupLifecycleUsers == 0 {
		if err := cgroupInitSubSysIDs(); err != nil {
			return err
		}
		if err := cgroupCssInitEventSync(); err != nil {
			return err
		}
	}
	cgroupLifecycleUsers++
	return nil
}

func releaseCgroupLifecycle() {
	cgroupLifecycleMu.Lock()
	defer cgroupLifecycleMu.Unlock()
	if cgroupLifecycleUsers == 0 {
		return
	}
	cgroupLifecycleUsers--
	if cgroupLifecycleUsers == 0 {
		closeCgroupLifecycle()
	}
}

func SubscribeMemoryCgroups(wake func()) (*MemoryCgroupSubscription, error) {
	hierarchy, err := memoryCgroupHierarchy()
	if err != nil {
		return nil, err
	}
	if err := acquireCgroupLifecycle(); err != nil {
		return nil, err
	}
	s := &MemoryCgroupSubscription{
		changes: make(chan MemoryCgroupChange, cgroupChangeQueueSize),
		wake:    wake, hierarchy: hierarchy,
	}
	memoryCgroupSubscribersMu.Lock()
	memoryCgroupSubscribers[s] = struct{}{}
	memoryCgroupSubscribersMu.Unlock()
	return s, nil
}

func (s *MemoryCgroupSubscription) Changes() <-chan MemoryCgroupChange {
	return s.changes
}

// TakeResync consumes only the current request; losses during a scan remain pending.
func (s *MemoryCgroupSubscription) TakeResync() bool {
	return s.resync.Swap(false)
}

func (s *MemoryCgroupSubscription) requestResync() {
	if !s.resync.Swap(true) {
		s.wake()
	}
}

func resyncMemoryCgroups() {
	memoryCgroupSubscribersMu.RLock()
	defer memoryCgroupSubscribersMu.RUnlock()
	for s := range memoryCgroupSubscribers {
		s.requestResync()
	}
}

func (s *MemoryCgroupSubscription) Close() {
	memoryCgroupSubscribersMu.Lock()
	_, exists := memoryCgroupSubscribers[s]
	delete(memoryCgroupSubscribers, s)
	memoryCgroupSubscribersMu.Unlock()
	if exists {
		releaseCgroupLifecycle()
	}
}

func memoryCgroupHierarchy() (int32, error) {
	if cgroups.CgroupMode() == cgroups.Unified {
		return 0, nil
	}
	file, err := os.Open(procfs.Path("cgroups"))
	if err != nil {
		return 0, err
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var name string
		var hierarchy, count, enabled int32
		if _, err := fmt.Sscanf(scanner.Text(), "%s %d %d %d", &name, &hierarchy, &count, &enabled); err == nil &&
			name == "memory" && hierarchy > 0 && enabled != 0 {
			return hierarchy, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("memory controller has no active cgroup v1 hierarchy")
}

func publishMemoryCgroupChange(data *containerCssPerfEvent, id string) {
	if data.Operation != abi.CgroupCSSOperationUpdate && data.Operation != abi.CgroupCSSOperationRemove {
		return
	}
	memoryCgroupSubscribersMu.RLock()
	defer memoryCgroupSubscribersMu.RUnlock()
	if len(memoryCgroupSubscribers) == 0 {
		return
	}
	if id == "" {
		return
	}
	change := MemoryCgroupChange{ContainerID: id, Removed: data.Operation == abi.CgroupCSSOperationRemove}
	for s := range memoryCgroupSubscribers {
		if data.CgroupRoot != s.hierarchy {
			continue
		}
		select {
		case s.changes <- change:
			s.wake()
		default:
			s.requestResync()
		}
	}
}

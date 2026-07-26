// Copyright 2025, 2026 The HuaTuo Authors
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
	"fmt"
	"sync"
	"testing"
)

// TestContainersCPUIdleConcurrentAccess verifies that the global
// containersCPUIdle map is safe under concurrent read/write/delete
// from multiple goroutines. Run with -race to detect data races.
func TestContainersCPUIdleConcurrentAccess(t *testing.T) {
	// Reset the global map to a clean state.
	containersCPUIdleMu.Lock()
	containersCPUIdle = make(containersCPUIdleMap)
	containersCPUIdleMu.Unlock()

	const (
		numWriters = 4
		numReaders = 4
		numDeletes = 2
		numEntries = 100
	)

	var wg sync.WaitGroup

	// Writers: insert entries into the global map.
	for w := 0; w < numWriters; w++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()
			for i := 0; i < numEntries; i++ {
				id := fmt.Sprintf("container-w%d-%d", writerID, i)
				containersCPUIdleMu.Lock()
				containersCPUIdle[id] = &containerCPUInfo{
					id:   id,
					path: fmt.Sprintf("/sys/fs/cgroup/%s", id),
				}
				containersCPUIdleMu.Unlock()
			}
		}(w)
	}

	// Readers: iterate over the map while writers are active.
	for r := 0; r < numReaders; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < numEntries; i++ {
				containersCPUIdleMu.Lock()
				for id, info := range containersCPUIdle {
					_ = id
					_ = info.alive
				}
				containersCPUIdleMu.Unlock()
			}
		}()
	}

	// Deleters: remove entries while others read/write.
	for d := 0; d < numDeletes; d++ {
		wg.Add(1)
		go func(delID int) {
			defer wg.Done()
			for i := 0; i < numEntries; i++ {
				id := fmt.Sprintf("container-w%d-%d", delID, i)
				containersCPUIdleMu.Lock()
				delete(containersCPUIdle, id)
				containersCPUIdleMu.Unlock()
			}
		}(d)
	}

	wg.Wait()
}

// TestContainersCPUIdleConcurrentUpdateAndDetect simulates the real
// production pattern: updateContainersCPUIdle and detectCPUIdleContainer
// called from different goroutine contexts on the same global map.
func TestContainersCPUIdleConcurrentUpdateAndDetect(t *testing.T) {
	containersCPUIdleMu.Lock()
	containersCPUIdle = make(containersCPUIdleMap)
	containersCPUIdleMu.Unlock()

	const numIterations = 50
	var wg sync.WaitGroup

	// Simulate updateContainersCPUIdle: writes entries.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numIterations; i++ {
			id := fmt.Sprintf("c-%d", i)
			containersCPUIdleMu.Lock()
			if existing, ok := containersCPUIdle[id]; ok {
				existing.alive = true
				existing.path = "/updated"
			} else {
				containersCPUIdle[id] = &containerCPUInfo{
					id:    id,
					path:  "/test",
					alive: true,
				}
			}
			containersCPUIdleMu.Unlock()
		}
	}()

	// Simulate detectCPUIdleContainer: iterates and deletes dead entries.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < numIterations; i++ {
			containersCPUIdleMu.Lock()
			for id, container := range containersCPUIdle {
				if !container.alive {
					delete(containersCPUIdle, id)
				} else {
					container.alive = false
				}
			}
			containersCPUIdleMu.Unlock()
		}
	}()

	wg.Wait()
}

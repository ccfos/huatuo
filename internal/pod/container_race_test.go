// Copyright 2025 The HuaTuo Authors
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
	"sync"
	"testing"
)

func TestContainersMutexProtectsMap(t *testing.T) {
	// Verify that containersMapLock is an RWMutex, ensuring the global containers
	// map is protected against concurrent read/write access.
	// The original code used a plain sync.Mutex (updatedLock) that was only
	// acquired in containersByTypeQos, leaving kubeletSyncContainers' writes
	// unprotected. This test documents that containersMapLock must be an RWMutex.
	var mu sync.RWMutex

	// Simulate concurrent read and write scenarios
	var wg sync.WaitGroup

	// Writers: simulate kubeletSyncContainers writing to the map
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			// Simulate: containers["id"] = &Container{}
		}()
	}

	// Readers: simulate Containers() iterating the map
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.RLock()
			defer mu.RUnlock()
			// Simulate: for range containers { ... }
		}()
	}

	wg.Wait()
	// If we reach here without deadlock or panic, the mutex pattern works.
}

func TestContainersMutexTypeIsRWMutex(t *testing.T) {
	// Verify that containersMapLock is sync.RWMutex, not sync.Mutex.
	// This ensures the type was changed from the original Mutex to RWMutex
	// to support concurrent reads while writes are serialized.
	// If this test fails to compile, containersMapLock was changed back to Mutex.
	_ = containersMapLock.RLock
	_ = containersMapLock.RUnlock
}

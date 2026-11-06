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
	"fmt"
	"testing"
)

func BenchmarkContainerSourceRefresh(b *testing.B) {
	initMu.Lock()
	previous := currContainerProvider
	currContainerProvider = containerProviderContainerd
	initMu.Unlock()
	b.Cleanup(func() { initMu.Lock(); currContainerProvider = previous; initMu.Unlock() })
	for _, size := range []int{1, 1024} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			ids := make([]string, size)
			for i := range ids {
				ids[i] = fmt.Sprintf("%064x", i)
			}
			list := runningPodListForTest(ids...)
			store := newTestContainerStore()
			source := containerSourceForTest(list.Items)
			manager := sourceManagerForTest(b, store, source)
			b.ReportAllocs()
			for b.Loop() {
				snapshot, err, _ := manager.discover(b.Context(), nil, false)
				if err != nil {
					b.Fatal(err)
				}
				store.commit(snapshot, nil)
			}
		})
	}
}

// BenchmarkSourceDiscovery measures the public discovery boundary and Store
// commit for metadata-only workloads, without hiding binding allocations.
func BenchmarkSourceDiscovery(b *testing.B) {
	for _, size := range []int{1, 1024} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			pods := make(map[string]*Pod, size)
			for index := range size {
				id := fmt.Sprintf("checkout-sandbox-%d", index)
				pods[id] = &Pod{ID: id, Type: TypeCubeSandbox}
			}
			source := &sourceFixture{kind: TypeCubeSandbox, discover: func(context.Context) (Discovery, error) { return Discovery{Pods: pods, Complete: true}, nil }}
			store := newTestContainerStore()
			manager, err := newSourceManager(store, source, ManagerOptions{})
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				snapshot, err, _ := manager.discover(b.Context(), nil, true)
				if err != nil {
					b.Fatal(err)
				}
				store.commit(snapshot, nil)
			}
		})
	}
}

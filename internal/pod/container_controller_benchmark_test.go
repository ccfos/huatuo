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

	corev1 "k8s.io/api/core/v1"
)

func BenchmarkContainerControllerRefresh(b *testing.B) {
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
			c := newContainerController(store)
			c.fetch = func(context.Context) (corev1.PodList, error) { return list, nil }
			resolves := 0
			c.resolve = func(id string, _ *corev1.Container, _ *corev1.ContainerStatus, _ *corev1.Pod) (*containerRecord, error) {
				resolves++
				return containerRecordForTest(id), nil
			}
			snapshot, err, _ := c.refresh(b.Context(), nil, true)
			if err != nil {
				b.Fatal(err)
			}
			store.commit(snapshot, nil)
			b.ReportAllocs()
			for b.Loop() {
				snapshot, err, _ := c.refresh(b.Context(), nil, false)
				if err != nil {
					b.Fatal(err)
				}
				store.commit(snapshot, nil)
			}
			if resolves != size {
				b.Fatalf("healthy containers were resolved again: got %d resolutions, want %d", resolves, size)
			}
		})
	}
}

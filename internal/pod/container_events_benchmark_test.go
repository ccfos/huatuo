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
	"testing"
)

func BenchmarkContainerEvents(b *testing.B) {
	for _, size := range []int{0, 1, 1024, 4096} {
		for _, subscribers := range []int{1, 8} {
			b.Run(fmt.Sprintf("containers=%d/subscribers=%d", size, subscribers), func(b *testing.B) {
				store := newTestContainerStore()
				for i := 0; i < size; i++ {
					id := fmt.Sprint(i)
					store.records[id] = containerRecordForTest(id)
				}
				subs := make([]*ContainerSubscription, subscribers)
				for i := range subs {
					subs[i] = subscribeForTest(b, store)
					_, _ = subs[i].DrainEvents()
				}
				record := containerRecordForTest("event")
				record.ref.Key.Generation = 1
				b.ReportAllocs()
				for b.Loop() {
					store.mu.Lock()
					store.publish(ContainerCreated, record)
					store.mu.Unlock()
					for _, sub := range subs {
						if _, err := sub.DrainEvents(); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}

func BenchmarkContainerFullUpdate(b *testing.B) {
	for _, size := range []int{0, 1, 1024, 4096} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			store := newTestContainerStore()
			for i := 0; i < size; i++ {
				id := fmt.Sprint(i)
				store.records[id] = containerRecordForTest(id)
			}
			sub := subscribeForTest(b, store)
			_, _ = sub.DrainEvents()
			b.ReportAllocs()
			for b.Loop() {
				store.mu.Lock()
				sub.needsFull = true
				store.mu.Unlock()
				update, err := sub.DrainEvents()
				if err != nil || len(update.Events) != size {
					b.Fatal("invalid full update")
				}
			}
		})
	}
}

// BenchmarkPodEvents measures the sandbox projection of the shared queue.
func BenchmarkPodEvents(b *testing.B) {
	for _, count := range []int{1, 8} {
		b.Run(fmt.Sprintf("subscribers=%d", count), func(b *testing.B) {
			store := newTestContainerStore()
			for index := range 4096 {
				id := fmt.Sprintf("checkout-sandbox-%d", index)
				store.records[id] = &containerRecord{pod: &Pod{ID: id, Type: TypeCubeSandbox}, ref: ContainerRef{Key: PodKey{ID: id, Generation: 1}}}
			}
			subscriptions := make([]*PodSubscription, count)
			for index := range subscriptions {
				subscription, err := store.subscribePods(b.Context())
				if err != nil {
					b.Fatal(err)
				}
				b.Cleanup(subscription.Close)
				_, _ = subscription.DrainEvents()
				subscriptions[index] = subscription
			}
			record := store.records["checkout-sandbox-0"]
			b.ReportAllocs()
			for b.Loop() {
				store.mu.Lock()
				store.publish(PodCreated, record)
				store.mu.Unlock()
				for _, subscription := range subscriptions {
					if _, err := subscription.DrainEvents(); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

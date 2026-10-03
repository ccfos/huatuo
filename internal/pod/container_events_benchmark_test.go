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
				ref := ContainerRef{Key: ContainerKey{ID: "event", Generation: 1}, MemoryCgroupPath: "/event"}
				b.ReportAllocs()
				for b.Loop() {
					store.mu.Lock()
					store.publish(ContainerCreated, ref)
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

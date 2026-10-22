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

package memorywatch

import (
	"context"
	"fmt"
	"testing"
)

// Exclude cgroup I/O and snapshot work to measure delivery to a separate consumer.
func BenchmarkMemoryWatchHandoff(b *testing.B) {
	for _, count := range []int{1, 64, 4096} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			w := &Watcher{
				options: Options{MaxCgroups: count},
				pending: make(map[TargetID]*target, count),
				batch:   make([]Event, 0, eventBatch),
				ready:   make(chan struct{}, 1), done: make(chan struct{}),
			}
			targets := make([]target, count)
			for i := range targets {
				targets[i].id = TargetID(i + 1)
			}
			ctx, cancel := context.WithCancel(b.Context())
			processed := make(chan error, 1)
			consumerDone := make(chan struct{})
			go func() {
				defer close(consumerDone)
				received := 0
				for {
					select {
					case <-ctx.Done():
						return
					case <-w.Notify():
						events, err := w.DrainEvents()
						if err != nil {
							processed <- err
							return
						}
						for i := range events {
							if events[i].TargetID == 0 {
								processed <- fmt.Errorf("invalid target ID")
								return
							}
							received++
						}
						if received == count {
							processed <- nil
							received = 0
						}
					}
				}
			}()
			defer func() {
				cancel()
				<-consumerDone
			}()
			b.ReportAllocs()
			for b.Loop() {
				for i := range targets {
					entry := &targets[i]
					event := Event{TargetID: entry.id, Kind: ThresholdObserved}
					if err := w.publish(entry, &event); err != nil {
						b.Fatal(err)
					}
				}
				if err := <-processed; err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*count), "ns/event")
		})
	}
}

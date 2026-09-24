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

package pod

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestContainerViewConcurrentCommitDrainAndClose(t *testing.T) {
	store := newTestContainerStore()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				ctx, cancel := context.WithCancel(t.Context())
				s, err := store.subscribe(ctx)
				if err != nil {
					t.Error(err)
					cancel()
					return
				}
				_, _ = s.DrainEvents()
				cancel()
				s.Close()
			}
		}()
	}
	for i := 0; i < 100; i++ {
		record := containerRecordForTest(fmt.Sprintf("%064x", i))
		store.commit(map[string]*containerRecord{record.ref.Key.ID: record}, nil)
	}
	wg.Wait()
	if len(store.subscribers) != 0 {
		t.Fatal("closed subscriptions retained")
	}
}

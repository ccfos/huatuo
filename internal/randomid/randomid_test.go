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

package randomid

import (
	"strings"
	"sync"
	"testing"
)

// newID fails the test when identifier generation errors.
func newID(t *testing.T) string {
	t.Helper()

	id, err := New()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	return id
}

func TestNewContract(t *testing.T) {
	// The identifier feeds CLI --task-id flags, in-process map keys, and
	// session/agent file names, so length and charset are part of the
	// contract, not an implementation detail.
	id := newID(t)

	if len(id) != length {
		t.Fatalf("len(New()) = %d, want %d", len(id), length)
	}

	for i, r := range id {
		if !strings.ContainsRune(charset, r) {
			t.Fatalf("New()[%d] = %q, not in the documented charset", i, r)
		}
	}
}

func TestNewUniqueness(t *testing.T) {
	const draws = 10_000

	seen := make(map[string]struct{}, draws)
	for i := 0; i < draws; i++ {
		id := newID(t)
		if _, dup := seen[id]; dup {
			t.Fatalf("New() returned duplicate %q after %d draws", id, i)
		}

		seen[id] = struct{}{}
	}
}

func TestNewConcurrentUniqueness(t *testing.T) {
	const (
		goroutines = 16
		perRoutine = 256
		totalDraws = goroutines * perRoutine
	)

	ids := make(chan string, totalDraws)
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perRoutine; i++ {
				id, err := New()
				if err != nil {
					t.Errorf("New() error = %v", err)
					return
				}

				ids <- id
			}
		}()
	}

	wg.Wait()
	close(ids)

	seen := make(map[string]struct{}, totalDraws)
	for id := range ids {
		if _, dup := seen[id]; dup {
			t.Fatalf("New() returned duplicate %q under concurrency", id)
		}

		seen[id] = struct{}{}
	}

	if len(seen) != totalDraws {
		t.Fatalf("unique identifiers = %d, want %d", len(seen), totalDraws)
	}
}

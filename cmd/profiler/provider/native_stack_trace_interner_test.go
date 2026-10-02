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

package provider

import (
	"slices"
	"testing"
)

func TestStackTraceInternerLookupOrAdd(t *testing.T) {
	var interner stackTraceInterner
	firstFrames := []string{"main", "work"}
	firstID := interner.LookupOrAdd(firstFrames)
	secondID := interner.LookupOrAdd([]string{"main", "work"})

	if firstID == 0 {
		t.Fatal("LookupOrAdd() returned the empty stack trace ID")
	}
	if secondID != firstID {
		t.Fatalf("LookupOrAdd() ID = %d, want %d", secondID, firstID)
	}
	if got := interner.Frames(firstID); !slices.Equal(got, firstFrames) {
		t.Fatalf("Frames(%d) = %q, want %q", firstID, got, firstFrames)
	}
}

func TestStackTraceInternerLookupOrAddEmpty(t *testing.T) {
	var interner stackTraceInterner
	if got := interner.LookupOrAdd(nil); got != 0 {
		t.Fatalf("LookupOrAdd(nil) = %d, want 0", got)
	}
	if interner.heads != nil || interner.traces != nil {
		t.Fatal("LookupOrAdd(nil) initialized stack trace state")
	}
	if got := interner.Frames(0); got != nil {
		t.Fatalf("Frames(0) = %q, want nil", got)
	}
}

func TestStackTraceInternerPreservesFrameBoundaries(t *testing.T) {
	var interner stackTraceInterner
	joinedID := interner.LookupOrAdd([]string{"foo;bar"})
	separateID := interner.LookupOrAdd([]string{"foo", "bar"})

	if joinedID == separateID {
		t.Fatalf("frame sequences received the same ID %d", joinedID)
	}
}

func TestStackTraceInternerResolvesHashCollisions(t *testing.T) {
	const collisionHash = 7
	var interner stackTraceInterner
	interner.initialize()
	firstID := interner.lookupOrAddHashed(collisionHash, []string{"first"})
	secondID := interner.lookupOrAddHashed(collisionHash, []string{"second"})
	repeatedID := interner.lookupOrAddHashed(collisionHash, []string{"first"})

	if firstID == secondID {
		t.Fatalf("colliding stack traces received the same ID %d", firstID)
	}
	if repeatedID != firstID {
		t.Fatalf("repeated colliding stack trace ID = %d, want %d", repeatedID, firstID)
	}
}

func TestStackTraceInternerReset(t *testing.T) {
	var interner stackTraceInterner
	interner.LookupOrAdd([]string{"main"})

	interner.Reset()

	if interner.heads != nil || interner.traces != nil {
		t.Fatal("Reset() retained stack trace state")
	}
	if got := interner.LookupOrAdd([]string{"work"}); got != 1 {
		t.Fatalf("LookupOrAdd() after Reset() = %d, want 1", got)
	}
}

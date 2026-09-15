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

package server

import (
	"fmt"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

func TestClientLimitersSharesLimiterPerKey(t *testing.T) {
	limiters := newClientLimiters(rate.Every(time.Hour), 1)
	now := time.Now()

	if !limiters.allow("10.0.0.1", now) {
		t.Fatal("first allow() = false, want true")
	}
	if limiters.allow("10.0.0.1", now) {
		t.Error("second allow() = true for same key, want false (burst exhausted)")
	}
	if !limiters.allow("10.0.0.2", now) {
		t.Error("allow() = false for a different key, want true (limiters are per client)")
	}
}

func TestClientLimitersEvictsLeastRecentlySeenAtCap(t *testing.T) {
	limiters := newClientLimiters(rate.Inf, 1)
	now := time.Now()

	if len(limiters.entries) > maxLimiters {
		t.Fatalf("initial entries=%d, want <= %d", len(limiters.entries), maxLimiters)
	}

	// Fill to the cap, then touch the oldest key so it is no longer the
	// least recently seen.
	for i := 0; i < maxLimiters; i++ {
		if !limiters.allow(keyForIndex(i), now) {
			t.Fatalf("allow(%q) = false with rate=Inf", keyForIndex(i))
		}
	}
	if len(limiters.entries) != maxLimiters {
		t.Fatalf("entries=%d at cap, want %d", len(limiters.entries), maxLimiters)
	}

	touched := keyForIndex(0)
	if !limiters.allow(touched, now) {
		t.Fatalf("allow(%q) = false with rate=Inf", touched)
	}

	// One more client forces an eviction: the untouched oldest key
	// (keyForIndex(1)) must be dropped, entries stay capped, and the
	// touched key must survive.
	if !limiters.allow("new-client", now) {
		t.Fatal("allow(new-client) = false with rate=Inf")
	}
	if len(limiters.entries) != maxLimiters {
		t.Fatalf("entries=%d after overflow, want %d", len(limiters.entries), maxLimiters)
	}
	if _, ok := limiters.entries[keyForIndex(1)]; ok {
		t.Error("least recently seen entry was not evicted")
	}
	if _, ok := limiters.entries[touched]; !ok {
		t.Error("recently seen entry was evicted")
	}
}

func keyForIndex(i int) string {
	return fmt.Sprintf("10.1.%d.%d:8080", i/254, i%254+1)
}

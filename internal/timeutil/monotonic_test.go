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

package timeutil

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestKtimeToTimeOffsets(t *testing.T) {
	// Keep clock sampling out of the arithmetic boundary cases.
	realtimeOffsetCache.mu.Lock()
	offsetNS, refreshedAt := realtimeOffsetCache.offsetNS, realtimeOffsetCache.refreshedAt
	realtimeOffsetCache.mu.Unlock()
	t.Cleanup(func() {
		realtimeOffsetCache.mu.Lock()
		defer realtimeOffsetCache.mu.Unlock()
		realtimeOffsetCache.offsetNS = offsetNS
		realtimeOffsetCache.refreshedAt = refreshedAt
	})

	tests := []struct {
		name        string
		monotonicNS uint64
		offset      int64
		want        int64
	}{
		{name: "positive offset", monotonicNS: 100, offset: 1000, want: 1100},
		{name: "updated offset", monotonicNS: 100, offset: 2000, want: 2100},
		{name: "negative offset", monotonicNS: 100, offset: -200, want: -100},
		{name: "maximum timestamp", monotonicNS: math.MaxInt64, want: math.MaxInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			realtimeOffsetCache.mu.Lock()
			realtimeOffsetCache.offsetNS = tt.offset
			realtimeOffsetCache.refreshedAt = time.Now()
			realtimeOffsetCache.mu.Unlock()
			got, err := KtimeToTime(tt.monotonicNS)
			if err != nil {
				t.Fatal(err)
			}
			if got.UnixNano() != tt.want || got.Location() != time.UTC {
				t.Fatalf("converted time = %v, want Unix nanoseconds %d in UTC", got, tt.want)
			}
		})
	}
}

func BenchmarkKtimeToTime(b *testing.B) {
	monotonicNS, err := MonotonicNowNS()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := KtimeToTime(monotonicNS); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkKtimeToTimestamp(b *testing.B) {
	monotonicNS, err := MonotonicNowNS()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := KtimeToTimestamp(monotonicNS); err != nil {
			b.Fatal(err)
		}
	}
}

func TestMonotonicRealtimeOffsetCacheRefresh(t *testing.T) {
	var cache monotonicRealtimeOffsetCache
	now := time.Now()
	calls := 0
	sample := func() (int64, error) { calls++; return int64(calls) * 1000, nil }
	first, err := cache.loadOrRefresh(func() time.Time { return now }, sample)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := cache.loadOrRefresh(func() time.Time { return now.Add(realtimeOffsetRefreshInterval - time.Nanosecond) }, sample)
	if err != nil {
		t.Fatal(err)
	}
	if first != 1000 || cached != first || calls != 1 {
		t.Fatalf("offsets = %d, %d; samples = %d", first, cached, calls)
	}
	refreshed, err := cache.loadOrRefresh(func() time.Time { return now.Add(realtimeOffsetRefreshInterval) }, sample)
	if err != nil {
		t.Fatal(err)
	}
	if refreshed != 2000 || calls != 2 {
		t.Fatalf("refreshed offset = %d; samples = %d", refreshed, calls)
	}
}

func TestMonotonicRealtimeOffsetCacheRefreshCompletion(t *testing.T) {
	var cache monotonicRealtimeOffsetCache
	now := time.Now()
	clock := func() time.Time { return now }
	samples := 0
	sample := func() (int64, error) {
		samples++
		now = now.Add(time.Minute)
		return 1234, nil
	}
	if _, err := cache.loadOrRefresh(clock, sample); err != nil {
		t.Fatal(err)
	}
	now = now.Add(realtimeOffsetRefreshInterval - time.Nanosecond)
	if _, err := cache.loadOrRefresh(clock, sample); err != nil {
		t.Fatal(err)
	}
	if samples != 1 {
		t.Fatalf("clock samples = %d, want 1 before expiry", samples)
	}
	now = now.Add(time.Nanosecond)
	if _, err := cache.loadOrRefresh(clock, sample); err != nil {
		t.Fatal(err)
	}
	if samples != 2 {
		t.Fatalf("clock samples = %d, want 2 at expiry", samples)
	}
}

func TestMonotonicRealtimeOffsetCacheRetriesFailedRefresh(t *testing.T) {
	var cache monotonicRealtimeOffsetCache
	now := time.Now()
	if _, err := cache.loadOrRefresh(func() time.Time { return now }, func() (int64, error) { return 1000, nil }); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("clock read failed")
	now = now.Add(realtimeOffsetRefreshInterval)
	if _, err := cache.loadOrRefresh(func() time.Time { return now }, func() (int64, error) { return 0, failure }); !errors.Is(err, failure) {
		t.Fatalf("refresh error = %v", err)
	}
	got, err := cache.loadOrRefresh(func() time.Time { return now }, func() (int64, error) { return 2000, nil })
	if err != nil || got != 2000 {
		t.Fatalf("retry = %d, %v", got, err)
	}
}

func TestMonotonicRealtimeOffsetCacheConcurrentReaders(t *testing.T) {
	var cache monotonicRealtimeOffsetCache
	var samples atomic.Int64
	now := time.Now()
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := cache.loadOrRefresh(func() time.Time { return now }, func() (int64, error) { samples.Add(1); return 1234, nil })
			if err != nil || got != 1234 {
				t.Errorf("load = %d, %v", got, err)
			}
		}()
	}
	wg.Wait()
	if got := samples.Load(); got != 1 {
		t.Fatalf("clock samples = %d, want 1", got)
	}
}

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
	"fmt"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const realtimeOffsetRefreshInterval = time.Hour

type monotonicRealtimeOffsetCache struct {
	mu          sync.Mutex
	offsetNS    int64
	refreshedAt time.Time
}

var realtimeOffsetCache monotonicRealtimeOffsetCache

func (c *monotonicRealtimeOffsetCache) loadOrRefresh(now func() time.Time, sample func() (int64, error)) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.refreshedAt.IsZero() && now().Sub(c.refreshedAt) < realtimeOffsetRefreshInterval {
		return c.offsetNS, nil
	}

	offsetNS, err := sample()
	if err != nil {
		return 0, err
	}
	c.offsetNS = offsetNS
	// Start the lifetime after successful sampling, not before waiting for the lock.
	c.refreshedAt = now()
	return offsetNS, nil
}

// MonotonicNowNS returns the clock used by bpf_ktime_get_ns().
func MonotonicNowNS() (uint64, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		return 0, fmt.Errorf("clock_gettime MONOTONIC: %w", err)
	}
	return uint64(unix.TimespecToNsec(ts)), nil
}

// MonotonicToTime converts a host CLOCK_MONOTONIC timestamp to UTC. The offset
// is refreshed on demand one hour after the last successful refresh.
// Historical events spanning a clock step or suspend cannot be reconstructed
// exactly from a monotonic timestamp alone.
// The caller must supply a valid kernel timestamp whose value and converted
// Unix nanoseconds fit in int64; this hot path does not check for overflow.
func MonotonicToTime(monotonicNS uint64) (time.Time, error) {
	offset, err := realtimeOffsetCache.loadOrRefresh(time.Now, MonoToRealOffset)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(0, int64(monotonicNS)+offset).UTC(), nil
}

// MonoToRealOffset brackets a CLOCK_MONOTONIC read between two
// CLOCK_REALTIME reads, up to 5 times, and keeps the tightest pair.
// Exits early when the bracket is already below 1µs.
func MonoToRealOffset() (int64, error) {
	const goodEnoughDeltaNs = 1000 // 1µs.

	var real1, mono, real2 unix.Timespec
	var bestDelta, offset int64

	for i := range 5 {
		if err := unix.ClockGettime(unix.CLOCK_REALTIME, &real1); err != nil {
			return 0, fmt.Errorf("clock_gettime REALTIME: %w", err)
		}
		if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &mono); err != nil {
			return 0, fmt.Errorf("clock_gettime MONOTONIC: %w", err)
		}
		if err := unix.ClockGettime(unix.CLOCK_REALTIME, &real2); err != nil {
			return 0, fmt.Errorf("clock_gettime REALTIME: %w", err)
		}

		r1 := unix.TimespecToNsec(real1)
		r2 := unix.TimespecToNsec(real2)
		delta := r2 - r1
		if i == 0 || delta < bestDelta {
			bestDelta = delta
			offset = (r1+r2)/2 - unix.TimespecToNsec(mono)
		}
		if bestDelta <= goodEnoughDeltaNs {
			break
		}
	}

	return offset, nil
}

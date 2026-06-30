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

var (
	offsetOnce        sync.Once
	offsetNanoseconds int64
	errOffsetOnce     error
)

// KtimeToTime converts a bpf_ktime_get_ns() nanosecond value to a
// wall-clock time.Time in UTC. The monotonic-to-realtime offset is
// sampled once on first call and cached for the process lifetime.
func KtimeToTime(ktimeNs uint64) (time.Time, error) {
	offsetOnce.Do(func() {
		offsetNanoseconds, errOffsetOnce = monoToRealOffset()
	})
	if errOffsetOnce != nil {
		return time.Time{}, errOffsetOnce
	}
	return time.Unix(0, int64(ktimeNs)+offsetNanoseconds).UTC(), nil
}

// monoToRealOffset brackets a CLOCK_MONOTONIC read between two
// CLOCK_REALTIME reads, up to 5 times, and keeps the tightest pair.
// Exits early when the bracket is already below 1µs.
func monoToRealOffset() (int64, error) {
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

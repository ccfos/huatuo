// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// This file decodes cumulative per-CPU throttle-wait counters.
// Count and wait share one aligned 64-bit word per CPU, read once to keep
// each IO's count paired with its wait time. The 26/38-bit split uses 10us
// wait units to keep snapshots compact: each wait loses less than 10us,
// and intervals exceeding either field's capacity may be undercounted.

package collector

import (
	"encoding/binary"
	"fmt"
)

const (
	throtlWaitValueSize    = 8
	throtlDelayedCountBits = 26
	throtlWait10USBits     = 38
	throtlDelayedCountMask = uint64(1<<throtlDelayedCountBits) - 1
	throtlWait10USMask     = uint64(1<<throtlWait10USBits) - 1
)

type throtlWaitCounters struct {
	DelayedCount uint64
	Wait10US     uint64
}

func decodeThrotlWaitCounters(
	data []byte,
	possibleCPUs int,
) ([]throtlWaitCounters, error) {
	if possibleCPUs <= 0 {
		return nil, fmt.Errorf("invalid possible CPU count: %d", possibleCPUs)
	}
	maxInt := int(^uint(0) >> 1)
	if possibleCPUs > maxInt/throtlWaitValueSize {
		return nil, fmt.Errorf("possible CPU count is too large: %d",
			possibleCPUs)
	}
	expectedSize := possibleCPUs * throtlWaitValueSize
	if len(data) != expectedSize {
		return nil, fmt.Errorf("data size %d, want %d for %d possible CPUs",
			len(data), expectedSize, possibleCPUs)
	}

	counters := make([]throtlWaitCounters, possibleCPUs)
	for cpu := range possibleCPUs {
		offset := cpu * throtlWaitValueSize
		packed := binary.LittleEndian.Uint64(
			data[offset : offset+throtlWaitValueSize])
		counters[cpu] = decodeThrotlWaitCounter(packed)
	}
	return counters, nil
}

func decodeThrotlWaitCounter(packed uint64) throtlWaitCounters {
	return throtlWaitCounters{
		DelayedCount: (packed >> throtlWait10USBits) &
			throtlDelayedCountMask,
		Wait10US: packed & throtlWait10USMask,
	}
}

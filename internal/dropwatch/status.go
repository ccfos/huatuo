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

package dropwatch

import (
	"encoding/binary"
	"fmt"

	"github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	perfStatusMapName     = "bpf_perf_out_dropwatch"
	rateLimitStateMapName = "bpf_rlimit_dropwatch"
)

// ReadStatus reads cumulative counters without resetting them. Map and reader
// counters are sampled separately, not as an atomic snapshot. It may run
// concurrently with ReadInto and remains available after context cancellation.
// On error, HasMapCounters is false and PerfLost and RateLimited are zeroed
// because their values are unavailable; LostSamples remains valid.
func (s *Tracer) ReadStatus() (status types.DropwatchStatus, returnErr error) {
	defer func() { status.LostSamples = s.lostSamples.Load() }()
	if s.isClosed.Load() {
		return types.DropwatchStatus{}, bpf.ErrClosed
	}
	key := make([]byte, 4)
	raw, err := s.bpf.ReadMap(s.perfStatusMap, key)
	if err != nil {
		return types.DropwatchStatus{}, fmt.Errorf(
			"read dropwatch BPF map %q: %w",
			perfStatusMapName,
			err,
		)
	}
	if len(raw) == 0 || len(raw)%abi.BPFPerfOutputStatsSize != 0 {
		return types.DropwatchStatus{}, fmt.Errorf(
			"decode dropwatch BPF map %q: value size %d is not a positive multiple of %d",
			perfStatusMapName,
			len(raw),
			abi.BPFPerfOutputStatsSize,
		)
	}

	for offset := 0; offset < len(raw); offset += abi.BPFPerfOutputStatsSize {
		status.PerfLost += binary.NativeEndian.Uint64(raw[offset:])
	}

	raw, err = s.bpf.ReadMap(s.rateLimitStateMap, key)
	if err != nil {
		return types.DropwatchStatus{}, fmt.Errorf(
			"read dropwatch BPF map %q: %w",
			rateLimitStateMapName,
			err,
		)
	}

	var state abi.BPFRatelimitEvent
	if _, err := binary.Decode(raw, binary.NativeEndian, &state); err != nil {
		return types.DropwatchStatus{}, fmt.Errorf(
			"decode dropwatch BPF map %q: %w",
			rateLimitStateMapName,
			err,
		)
	}

	status.RateLimited = state.TotalMissed
	status.HasMapCounters = true
	return status, nil
}

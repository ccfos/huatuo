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

package netcorrelate

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/timeutil"
	"huatuo-bamai/pkg/types"
)

const (
	dropwatchActiveEpochMapName = "dropwatch_active_epoch"
	//nolint:gosec // BPF map name, not a credential.
	dropwatchEpochStatsMapName = "dropwatch_epoch_stats"
	dropwatchEpochSlotCount    = uint32(abi.DropwatchEpochSlotCount)
	dropwatchEpochStatsSize    = abi.DropwatchPerfEpochStatsSize
)

type perfEpochStats struct {
	inflight    uint64
	perfLost    uint64
	rateLimited uint64
}

type perfDrainState struct {
	frozenSlot uint32
	cutoffNS   uint64
}

// PerfBarrier establishes a drained ktime boundary for one dropwatch reader.
// Its methods have one owner and must not be called concurrently.
type PerfBarrier struct {
	obj              bpf.BPF
	activeEpochMapID uint32
	epochStatsMapID  uint32
	baselines        [dropwatchEpochSlotCount]perfEpochStats
	totals           perfEpochStats
	drain            perfDrainState
	draining         bool
}

// NewPerfBarrier validates the epoch maps before dropwatch is attached.
func NewPerfBarrier(obj bpf.BPF) (*PerfBarrier, error) {
	if obj == nil {
		return nil, errors.New("netcorrelate: create perf barrier: nil BPF object")
	}

	barrier := &PerfBarrier{
		obj:              obj,
		activeEpochMapID: obj.MapIDByName(dropwatchActiveEpochMapName),
		epochStatsMapID:  obj.MapIDByName(dropwatchEpochStatsMapName),
	}
	if barrier.activeEpochMapID == 0 {
		return nil, fmt.Errorf(
			"netcorrelate: create perf barrier: required map %q not found",
			dropwatchActiveEpochMapName,
		)
	}
	if barrier.epochStatsMapID == 0 {
		return nil, fmt.Errorf(
			"netcorrelate: create perf barrier: required map %q not found",
			dropwatchEpochStatsMapName,
		)
	}

	for slot := uint32(0); slot < dropwatchEpochSlotCount; slot++ {
		stats, err := barrier.readEpochStats(slot)
		if err != nil {
			return nil, err
		}
		if stats.inflight != 0 {
			return nil, fmt.Errorf(
				"netcorrelate: create perf barrier: epoch %d has %d events in flight",
				slot,
				stats.inflight,
			)
		}
		barrier.baselines[slot] = stats
	}
	if err := barrier.writeActiveEpoch(0); err != nil {
		return nil, err
	}

	return barrier, nil
}

// BeginPerfDrain freezes the current epoch at a monotonic ktime boundary.
func (b *PerfBarrier) BeginPerfDrain() error {
	if b == nil {
		return errors.New("netcorrelate: begin perf drain: nil barrier")
	}
	if b.draining {
		return errors.New("netcorrelate: begin perf drain: drain already active")
	}

	activeSlot, err := b.readActiveEpoch()
	if err != nil {
		return err
	}
	inactiveSlot := activeSlot ^ 1
	inactiveStats, err := b.readEpochStats(inactiveSlot)
	if err != nil {
		return err
	}
	if inactiveStats.inflight != 0 {
		return fmt.Errorf(
			"netcorrelate: begin perf drain: inactive epoch %d has %d events in flight",
			inactiveSlot,
			inactiveStats.inflight,
		)
	}
	if inactiveStats.perfLost != b.baselines[inactiveSlot].perfLost ||
		inactiveStats.rateLimited != b.baselines[inactiveSlot].rateLimited {
		return fmt.Errorf(
			"netcorrelate: begin perf drain: inactive epoch %d counters changed",
			inactiveSlot,
		)
	}
	b.baselines[inactiveSlot] = inactiveStats

	cutoffNS, err := timeutil.MonotonicNowNS()
	if err != nil {
		return fmt.Errorf("netcorrelate: begin perf drain: %w", err)
	}
	if cutoffNS == 0 {
		return errors.New("netcorrelate: begin perf drain: monotonic cutoff is zero")
	}
	if err := b.writeActiveEpoch(inactiveSlot); err != nil {
		return err
	}
	b.drain = perfDrainState{frozenSlot: activeSlot, cutoffNS: cutoffNS}
	b.draining = true
	return nil
}

// IsPerfDrainComplete reports whether the frozen BPF epoch is idle.
func (b *PerfBarrier) IsPerfDrainComplete() (bool, error) {
	if b == nil {
		return false, errors.New("netcorrelate: check perf drain: nil barrier")
	}
	if !b.draining {
		return false, errors.New("netcorrelate: check perf drain: no active drain")
	}

	stats, err := b.readEpochStats(b.drain.frozenSlot)
	if err != nil {
		return false, err
	}
	return stats.inflight == 0, nil
}

// CompletePerfDrain records the frozen epoch after the reader reaches
// ErrPerfFlushed. The returned counters are cumulative for this barrier.
func (b *PerfBarrier) CompletePerfDrain() (types.DropwatchPerfStatus, error) {
	if b == nil {
		return types.DropwatchPerfStatus{}, errors.New(
			"netcorrelate: complete perf drain: nil barrier",
		)
	}
	if !b.draining {
		return types.DropwatchPerfStatus{}, errors.New(
			"netcorrelate: complete perf drain: no active drain",
		)
	}

	stats, err := b.readEpochStats(b.drain.frozenSlot)
	if err != nil {
		return types.DropwatchPerfStatus{}, err
	}
	if stats.inflight != 0 {
		return types.DropwatchPerfStatus{}, fmt.Errorf(
			"netcorrelate: complete perf drain: epoch %d has %d events in flight",
			b.drain.frozenSlot,
			stats.inflight,
		)
	}

	baseline := b.baselines[b.drain.frozenSlot]
	if stats.perfLost < baseline.perfLost || stats.rateLimited < baseline.rateLimited {
		return types.DropwatchPerfStatus{}, fmt.Errorf(
			"netcorrelate: complete perf drain: epoch %d counters regressed",
			b.drain.frozenSlot,
		)
	}
	perfLost, carry := bits.Add64(
		b.totals.perfLost,
		stats.perfLost-baseline.perfLost,
		0,
	)
	if carry != 0 {
		return types.DropwatchPerfStatus{}, errors.New(
			"netcorrelate: complete perf drain: perf lost counter overflow",
		)
	}
	rateLimited, carry := bits.Add64(
		b.totals.rateLimited,
		stats.rateLimited-baseline.rateLimited,
		0,
	)
	if carry != 0 {
		return types.DropwatchPerfStatus{}, errors.New(
			"netcorrelate: complete perf drain: rate-limit counter overflow",
		)
	}

	b.baselines[b.drain.frozenSlot] = stats
	b.totals.perfLost = perfLost
	b.totals.rateLimited = rateLimited
	status := types.DropwatchPerfStatus{
		DrainedThroughKtimeNS: b.drain.cutoffNS - 1,
		PerfLost:              perfLost,
		RateLimited:           rateLimited,
	}
	b.drain = perfDrainState{}
	b.draining = false
	return status, nil
}

func (b *PerfBarrier) readActiveEpoch() (uint32, error) {
	key := make([]byte, 4)
	value, err := b.obj.ReadMap(b.activeEpochMapID, key)
	if err != nil {
		return 0, fmt.Errorf(
			"netcorrelate: read map %q: %w",
			dropwatchActiveEpochMapName,
			err,
		)
	}
	if len(value) != 4 {
		return 0, fmt.Errorf(
			"netcorrelate: decode map %q: value size %d, want 4",
			dropwatchActiveEpochMapName,
			len(value),
		)
	}
	activeSlot := binary.NativeEndian.Uint32(value)
	if activeSlot >= dropwatchEpochSlotCount {
		return 0, fmt.Errorf(
			"netcorrelate: decode map %q: epoch %d outside [0,%d)",
			dropwatchActiveEpochMapName,
			activeSlot,
			dropwatchEpochSlotCount,
		)
	}
	return activeSlot, nil
}

func (b *PerfBarrier) writeActiveEpoch(slot uint32) error {
	key := make([]byte, 4)
	value := make([]byte, 4)
	binary.NativeEndian.PutUint32(value, slot)
	if err := b.obj.WriteMapItems(
		b.activeEpochMapID,
		[]bpf.MapItem{{Key: key, Value: value}},
	); err != nil {
		return fmt.Errorf(
			"netcorrelate: write map %q: %w",
			dropwatchActiveEpochMapName,
			err,
		)
	}
	return nil
}

func (b *PerfBarrier) readEpochStats(slot uint32) (perfEpochStats, error) {
	key := make([]byte, 4)
	binary.NativeEndian.PutUint32(key, slot)
	value, err := b.obj.ReadMap(b.epochStatsMapID, key)
	if err != nil {
		return perfEpochStats{}, fmt.Errorf(
			"netcorrelate: read map %q epoch %d: %w",
			dropwatchEpochStatsMapName,
			slot,
			err,
		)
	}
	if len(value) == 0 || len(value)%dropwatchEpochStatsSize != 0 {
		return perfEpochStats{}, fmt.Errorf(
			"netcorrelate: decode map %q epoch %d: value size %d is not a positive multiple of %d",
			dropwatchEpochStatsMapName,
			slot,
			len(value),
			dropwatchEpochStatsSize,
		)
	}

	var total perfEpochStats
	for offset := 0; offset < len(value); offset += dropwatchEpochStatsSize {
		var raw abi.DropwatchPerfEpochStats
		if _, err := binary.Decode(
			value[offset:offset+dropwatchEpochStatsSize],
			binary.NativeEndian,
			&raw,
		); err != nil {
			return perfEpochStats{}, fmt.Errorf(
				"netcorrelate: decode map %q epoch %d: %w",
				dropwatchEpochStatsMapName,
				slot,
				err,
			)
		}
		var carry uint64
		total.inflight, carry = bits.Add64(
			total.inflight,
			raw.Inflight,
			0,
		)
		if carry != 0 {
			return perfEpochStats{}, fmt.Errorf(
				"netcorrelate: decode map %q epoch %d: inflight counter overflow",
				dropwatchEpochStatsMapName,
				slot,
			)
		}
		total.perfLost, carry = bits.Add64(
			total.perfLost,
			raw.PerfLost,
			0,
		)
		if carry != 0 {
			return perfEpochStats{}, fmt.Errorf(
				"netcorrelate: decode map %q epoch %d: perf lost counter overflow",
				dropwatchEpochStatsMapName,
				slot,
			)
		}
		total.rateLimited, carry = bits.Add64(
			total.rateLimited,
			raw.RateLimited,
			0,
		)
		if carry != 0 {
			return perfEpochStats{}, fmt.Errorf(
				"netcorrelate: decode map %q epoch %d: rate-limit counter overflow",
				dropwatchEpochStatsMapName,
				slot,
			)
		}
	}
	return total, nil
}

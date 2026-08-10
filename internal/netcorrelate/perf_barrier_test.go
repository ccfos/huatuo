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
	"math"
	"testing"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	fakeActiveEpochMapID uint32 = 1
	fakeEpochStatsMapID  uint32 = 2
)

type fakePerfBarrierBPF struct {
	bpf.BPF

	active       uint32
	stats        [dropwatchEpochSlotCount][]perfEpochStats
	readErr      error
	writeErr     error
	hasActiveMap bool
	hasStatsMap  bool
}

func newFakePerfBarrierBPF(cpuCount int) *fakePerfBarrierBPF {
	fake := &fakePerfBarrierBPF{hasActiveMap: true, hasStatsMap: true}
	for slot := range fake.stats {
		fake.stats[slot] = make([]perfEpochStats, cpuCount)
	}
	return fake
}

func (f *fakePerfBarrierBPF) MapIDByName(name string) uint32 {
	switch name {
	case dropwatchActiveEpochMapName:
		if f.hasActiveMap {
			return fakeActiveEpochMapID
		}
	case dropwatchEpochStatsMapName:
		if f.hasStatsMap {
			return fakeEpochStatsMapID
		}
	}
	return 0
}

func (f *fakePerfBarrierBPF) ReadMap(mapID uint32, key []byte) ([]byte, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	switch mapID {
	case fakeActiveEpochMapID:
		value := make([]byte, 4)
		binary.NativeEndian.PutUint32(value, f.active)
		return value, nil
	case fakeEpochStatsMapID:
		slot := binary.NativeEndian.Uint32(key)
		if slot >= dropwatchEpochSlotCount {
			return nil, errors.New("invalid fake epoch")
		}
		value := make([]byte, len(f.stats[slot])*dropwatchEpochStatsSize)
		for cpu, stats := range f.stats[slot] {
			offset := cpu * dropwatchEpochStatsSize
			raw := abi.DropwatchPerfEpochStats{
				Inflight:    stats.inflight,
				PerfLost:    stats.perfLost,
				RateLimited: stats.rateLimited,
			}
			if _, err := binary.Encode(
				value[offset:offset+dropwatchEpochStatsSize],
				binary.NativeEndian,
				raw,
			); err != nil {
				return nil, err
			}
		}
		return value, nil
	default:
		return nil, errors.New("invalid fake map")
	}
}

func (f *fakePerfBarrierBPF) WriteMapItems(mapID uint32, items []bpf.MapItem) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	if mapID != fakeActiveEpochMapID || len(items) != 1 || len(items[0].Value) != 4 {
		return errors.New("invalid fake map write")
	}
	f.active = binary.NativeEndian.Uint32(items[0].Value)
	return nil
}

func TestPerfBarrierAccumulatesPerSlotDeltas(t *testing.T) {
	fake := newFakePerfBarrierBPF(2)
	fake.active = 1
	fake.stats[0][0] = perfEpochStats{perfLost: 10, rateLimited: 20}
	fake.stats[1][0] = perfEpochStats{perfLost: 30, rateLimited: 40}

	barrier, err := NewPerfBarrier(fake)
	require.NoError(t, err)
	require.Zero(t, fake.active)

	fake.stats[0][0] = perfEpochStats{perfLost: 12, rateLimited: 23}
	fake.stats[0][1] = perfEpochStats{perfLost: 1, rateLimited: 4}
	require.NoError(t, barrier.BeginPerfDrain())
	require.Equal(t, uint32(1), fake.active)

	complete, err := barrier.IsPerfDrainComplete()
	require.NoError(t, err)
	require.True(t, complete)
	first, err := barrier.CompletePerfDrain()
	require.NoError(t, err)
	assert.Positive(t, first.DrainedThroughKtimeNS)
	assert.Equal(t, uint64(3), first.PerfLost)
	assert.Equal(t, uint64(7), first.RateLimited)

	fake.stats[1][0] = perfEpochStats{perfLost: 31, rateLimited: 45}
	fake.stats[1][1] = perfEpochStats{perfLost: 2, rateLimited: 1}
	require.NoError(t, barrier.BeginPerfDrain())
	require.Zero(t, fake.active)

	second, err := barrier.CompletePerfDrain()
	require.NoError(t, err)
	assert.GreaterOrEqual(t, second.DrainedThroughKtimeNS, first.DrainedThroughKtimeNS)
	assert.Equal(t, uint64(6), second.PerfLost)
	assert.Equal(t, uint64(13), second.RateLimited)
}

func TestPerfBarrierWaitsForFrozenEpoch(t *testing.T) {
	fake := newFakePerfBarrierBPF(1)
	barrier, err := NewPerfBarrier(fake)
	require.NoError(t, err)
	require.NoError(t, barrier.BeginPerfDrain())

	fake.stats[0][0].inflight = 1
	complete, err := barrier.IsPerfDrainComplete()
	require.NoError(t, err)
	assert.False(t, complete)
	_, err = barrier.CompletePerfDrain()
	assert.ErrorContains(t, err, "events in flight")

	fake.stats[0][0].inflight = 0
	complete, err = barrier.IsPerfDrainComplete()
	require.NoError(t, err)
	require.True(t, complete)
	_, err = barrier.CompletePerfDrain()
	require.NoError(t, err)
}

func TestPerfBarrierRejectsInvalidState(t *testing.T) {
	t.Run("map read failure", func(t *testing.T) {
		fake := newFakePerfBarrierBPF(1)
		fake.readErr = errors.New("read failed")
		_, err := NewPerfBarrier(fake)
		assert.ErrorContains(t, err, "read failed")
	})

	t.Run("map write failure", func(t *testing.T) {
		fake := newFakePerfBarrierBPF(1)
		fake.writeErr = errors.New("write failed")
		_, err := NewPerfBarrier(fake)
		assert.ErrorContains(t, err, "write failed")
	})

	t.Run("nil object", func(t *testing.T) {
		_, err := NewPerfBarrier(nil)
		assert.ErrorContains(t, err, "nil BPF object")
	})

	t.Run("missing epoch map", func(t *testing.T) {
		fake := newFakePerfBarrierBPF(1)
		fake.hasStatsMap = false
		_, err := NewPerfBarrier(fake)
		assert.ErrorContains(t, err, dropwatchEpochStatsMapName)
	})

	t.Run("initial inflight", func(t *testing.T) {
		fake := newFakePerfBarrierBPF(1)
		fake.stats[1][0].inflight = 1
		_, err := NewPerfBarrier(fake)
		assert.ErrorContains(t, err, "events in flight")
	})

	t.Run("overlapping drain", func(t *testing.T) {
		fake := newFakePerfBarrierBPF(1)
		barrier, err := NewPerfBarrier(fake)
		require.NoError(t, err)
		require.NoError(t, barrier.BeginPerfDrain())
		assert.ErrorContains(t, barrier.BeginPerfDrain(), "already active")
	})

	t.Run("inactive epoch changed", func(t *testing.T) {
		fake := newFakePerfBarrierBPF(1)
		barrier, err := NewPerfBarrier(fake)
		require.NoError(t, err)
		fake.stats[1][0].perfLost = 1
		assert.ErrorContains(t, barrier.BeginPerfDrain(), "counters changed")
	})

	t.Run("no active drain", func(t *testing.T) {
		fake := newFakePerfBarrierBPF(1)
		barrier, err := NewPerfBarrier(fake)
		require.NoError(t, err)
		_, err = barrier.IsPerfDrainComplete()
		assert.ErrorContains(t, err, "no active drain")
		_, err = barrier.CompletePerfDrain()
		assert.ErrorContains(t, err, "no active drain")
	})
}

func TestPerfBarrierRejectsCounterCorruption(t *testing.T) {
	t.Run("regression", func(t *testing.T) {
		fake := newFakePerfBarrierBPF(1)
		barrier, err := NewPerfBarrier(fake)
		require.NoError(t, err)
		barrier.baselines[0].perfLost = 2
		require.NoError(t, barrier.BeginPerfDrain())
		_, err = barrier.CompletePerfDrain()
		assert.ErrorContains(t, err, "counters regressed")
	})

	t.Run("overflow", func(t *testing.T) {
		fake := newFakePerfBarrierBPF(1)
		barrier, err := NewPerfBarrier(fake)
		require.NoError(t, err)
		barrier.totals.perfLost = math.MaxUint64
		fake.stats[0][0].perfLost = 1
		require.NoError(t, barrier.BeginPerfDrain())
		_, err = barrier.CompletePerfDrain()
		assert.ErrorContains(t, err, "counter overflow")
	})
}

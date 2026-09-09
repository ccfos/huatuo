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

package autotracing

import (
	"context"
	"encoding/binary"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	ibpf "github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func packSchedBlameSliceForTest(
	duration uint64,
	waitingTargets uint32,
	cssID uint16,
) schedBlamePackedSlice {
	return schedBlamePackedSlice{
		PackedBase: duration<<32 |
			uint64(waitingTargets&schedBlameBaseBitmapMask)<<
				schedBlameCSSIDBits |
			uint64(cssID),
	}
}

func encodeSchedBlameIdentityForTest(identity schedBlameIdentity) []byte {
	raw := make([]byte, schedBlamePerfRawSampleSize(schedBlameIdentitySize))
	binary.LittleEndian.PutUint16(raw[:2], identity.Magic)
	binary.LittleEndian.PutUint16(raw[2:4], identity.CSSID)
	binary.LittleEndian.PutUint64(raw[8:16], identity.Cgid)
	return raw
}

func encodeSchedBlameThrottleForTest(throttle schedBlameThrottle) []byte {
	raw := make([]byte, schedBlamePerfRawSampleSize(schedBlameThrottleSize))
	binary.LittleEndian.PutUint16(raw[:2], throttle.Magic)
	binary.LittleEndian.PutUint16(raw[2:4], throttle.DenseID)
	binary.LittleEndian.PutUint32(raw[4:8], throttle.DurationNs)
	return raw
}

func encodeSchedBlameBatchForTest(
	packedSlices []schedBlamePackedSlice,
	extraBytes int,
) []byte {
	count := uint32(len(packedSlices))
	capacity := schedBlameBatchCapacity(count)
	payloadSize := schedBlameBatchHeaderSize +
		capacity*schedBlameSliceSize
	rawSize := schedBlamePerfRawSampleSize(payloadSize) + extraBytes
	raw := make([]byte, 0, rawSize)
	raw = binary.LittleEndian.AppendUint16(raw, schedBlameBatchMagic)
	raw = binary.LittleEndian.AppendUint16(
		raw,
		schedBlameExtraBitmapU64Count,
	)
	raw = binary.LittleEndian.AppendUint32(raw, count)
	for _, packedSlice := range packedSlices {
		raw = binary.LittleEndian.AppendUint64(raw, packedSlice.PackedBase)
		for _, extraBitmap := range &packedSlice.BitmapExtra {
			raw = binary.LittleEndian.AppendUint64(raw, extraBitmap)
		}
	}
	raw = append(raw, make([]byte, rawSize-len(raw))...)
	return raw
}

func newSchedBlameStateForTest(
	sliceDropPercent uint32,
	targetCgid uint64,
) *schedBlameState {
	state := newSchedBlameState(sliceDropPercent)
	targets := [schedBlameMaxTargets]schedBlameTarget{}
	targets[schedBlameHighlightIndex] = schedBlameTarget{
		cgid:        targetCgid,
		containerID: "target-container",
		name:        "target",
		cgroupPath:  "/target",
	}
	state.setTargets(&targets)
	return state
}

func TestSchedBlameWireLayout(t *testing.T) {
	assert.Equal(t, 8*(1+schedBlameExtraBitmapU64Count),
		schedBlameSliceSize)
	assert.Equal(t, 8, schedBlameBatchHeaderSize)
	assert.Equal(t, 8+schedBlameMaxBatchSlices*schedBlameSliceSize,
		schedBlameBatchValueSize)
	assert.Equal(t, 16, binary.Size(schedBlameIdentity{}))
	assert.Equal(t, 8, binary.Size(schedBlameThrottle{}))
	assert.Equal(t, 4096, schedBlameMaxCSSIDs)
	assert.Equal(t, schedBlameBaseBitmapBits+
		64*schedBlameExtraBitmapU64Count, schedBlameMaxTargets)
	assert.Equal(t, 12, schedBlamePerfRawSampleSize(8))
	assert.Equal(t, 20, schedBlamePerfRawSampleSize(16))
}

func TestDecodeSchedBlameIdentity(t *testing.T) {
	want := schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: 1057,
		Cgid:  0xff00112233445566,
	}
	raw := encodeSchedBlameIdentityForTest(want)
	record := new(schedBlameRecord)

	require.NoError(t, decodeSchedBlameRecord(raw, record))
	assert.Equal(t, schedBlameRecordIdentity, record.kind)
	assert.Equal(t, want, record.identity)
}

func TestDecodeSchedBlameIdentityRejectsUnexpectedSize(t *testing.T) {
	want := schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: 1057,
		Cgid:  0xff00112233445566,
	}
	raw := append(encodeSchedBlameIdentityForTest(want), 0)
	record := new(schedBlameRecord)

	assert.EqualError(
		t,
		decodeSchedBlameRecord(raw, record),
		fmt.Sprintf("unexpected sched-blame record size: %d", len(raw)),
	)
}

func TestDecodeSchedBlameThrottle(t *testing.T) {
	want := schedBlameThrottle{
		Magic:      schedBlameThrottleMagic,
		DenseID:    7,
		DurationNs: 123_456,
	}
	raw := encodeSchedBlameThrottleForTest(want)
	record := new(schedBlameRecord)

	require.NoError(t, decodeSchedBlameRecord(raw, record))
	assert.Equal(t, schedBlameRecordThrottle, record.kind)
	assert.Equal(t, want, record.throttle)
}

func TestDecodeSchedBlameThrottleRejectsInvalidDenseID(t *testing.T) {
	want := schedBlameThrottle{
		Magic:      schedBlameThrottleMagic,
		DenseID:    schedBlameMaxTargets,
		DurationNs: 123_456,
	}
	record := new(schedBlameRecord)

	assert.EqualError(
		t,
		decodeSchedBlameRecord(encodeSchedBlameThrottleForTest(want), record),
		fmt.Sprintf(
			"invalid sched-blame throttle dense ID: %d",
			schedBlameMaxTargets,
		),
	)
}

func TestDecodeSchedBlameThrottleRejectsUnexpectedPadding(t *testing.T) {
	want := schedBlameThrottle{
		Magic:      schedBlameThrottleMagic,
		DenseID:    7,
		DurationNs: 123_456,
	}
	raw := append(encodeSchedBlameThrottleForTest(want), 0)
	record := new(schedBlameRecord)

	assert.EqualError(
		t,
		decodeSchedBlameRecord(raw, record),
		fmt.Sprintf("unexpected sched-blame throttle size: %d", len(raw)),
	)
}

func TestDecodeSchedBlameThrottleRejectsUnexpectedSize(t *testing.T) {
	for _, size := range []int{
		schedBlameThrottleSize - 1,
		schedBlamePerfRawSampleSize(schedBlameThrottleSize) + 1,
	} {
		raw := make([]byte, size)
		binary.LittleEndian.PutUint16(raw[:2], schedBlameThrottleMagic)
		record := new(schedBlameRecord)

		assert.EqualError(
			t,
			decodeSchedBlameRecord(raw, record),
			fmt.Sprintf(
				"unexpected sched-blame throttle size: %d",
				size,
			),
		)
	}
}

func TestDecodeSchedBlameBatch(t *testing.T) {
	packedSlices := []schedBlamePackedSlice{
		packSchedBlameSliceForTest(20, 1<<3, 0x111),
		packSchedBlameSliceForTest(30, 1<<2, 0x222),
		packSchedBlameSliceForTest(40, 1<<1, 0x333),
	}
	raw := encodeSchedBlameBatchForTest(packedSlices, 0)
	record := new(schedBlameRecord)

	require.NoError(t, decodeSchedBlameRecord(raw, record))
	assert.Equal(t, schedBlameRecordBatch, record.kind)
	assert.Equal(t, uint32(len(packedSlices)), record.batch.count)
	for index, packedSlice := range packedSlices {
		assert.Equal(t, packedSlice, record.batch.packedSlices[index])
	}
	cssID, duration := schedBlameUnpackSliceBase(
		&record.batch.packedSlices[0],
	)
	assert.Equal(t, uint16(0x111), cssID)
	assert.True(t, schedBlameSliceHasTarget(
		&record.batch.packedSlices[0],
		3,
	))
	assert.Equal(t, uint64(20), duration)
}

func TestDecodeSchedBlameBatchCapacityTiers(t *testing.T) {
	for _, count := range []int{1, 2, 3, 4, 5, 8, 9, 16, 17, 32, 33, 64, 65, 128} {
		t.Run(fmt.Sprintf("count_%d", count), func(t *testing.T) {
			packedSlices := make([]schedBlamePackedSlice, count)
			for index := range packedSlices {
				packedSlices[index].PackedBase = uint64(index + 1)
			}
			raw := encodeSchedBlameBatchForTest(packedSlices, 0)
			record := new(schedBlameRecord)

			require.NoError(t, decodeSchedBlameRecord(raw, record))
			assert.Equal(t, schedBlameRecordBatch, record.kind)
			assert.Equal(t, uint32(count), record.batch.count)
			for index, packedSlice := range packedSlices {
				assert.Equal(t, packedSlice,
					record.batch.packedSlices[index])
			}
		})
	}
}

func TestDecodeSchedBlameBatchRejectsUnexpectedPadding(t *testing.T) {
	packedSlice := packSchedBlameSliceForTest(20, 1<<3, 0x111)
	raw := encodeSchedBlameBatchForTest(
		[]schedBlamePackedSlice{packedSlice},
		1,
	)
	record := new(schedBlameRecord)

	assert.EqualError(
		t,
		decodeSchedBlameRecord(raw, record),
		fmt.Sprintf(
			"unexpected sched-blame batch size: count=1 size=%d",
			len(raw),
		),
	)
}

func TestDecodeSchedBlameOneSliceBatchUsesMagic(t *testing.T) {
	raw := encodeSchedBlameBatchForTest(
		[]schedBlamePackedSlice{{PackedBase: 1}},
		0,
	)
	record := new(schedBlameRecord)

	require.NoError(t, decodeSchedBlameRecord(raw, record))
	assert.Equal(t, schedBlameRecordBatch, record.kind)
	assert.Equal(t, uint64(1), record.batch.packedSlices[0].PackedBase)
}

func TestDecodeSchedBlameRejectsInvalidMagic(t *testing.T) {
	raw := make([]byte, schedBlameIdentitySize)
	binary.LittleEndian.PutUint16(raw[:2], 0x1234)
	record := new(schedBlameRecord)

	assert.EqualError(
		t,
		decodeSchedBlameRecord(raw, record),
		"invalid sched-blame record magic: 0x1234",
	)
}

func TestDecodeSchedBlameRejectsInvalidBatchCount(t *testing.T) {
	for _, count := range []uint32{0, schedBlameMaxBatchSlices + 1} {
		raw := make([]byte, schedBlameBatchHeaderSize)
		binary.LittleEndian.PutUint16(raw[:2], schedBlameBatchMagic)
		binary.LittleEndian.PutUint16(
			raw[2:4],
			schedBlameExtraBitmapU64Count,
		)
		binary.LittleEndian.PutUint32(raw[4:8], count)
		record := new(schedBlameRecord)

		assert.EqualError(
			t,
			decodeSchedBlameRecord(raw, record),
			fmt.Sprintf("invalid sched-blame batch count: %d", count),
		)
	}
}

func TestDecodeSchedBlameRejectsIncompatibleExtraBitmapCount(t *testing.T) {
	raw := encodeSchedBlameBatchForTest(
		[]schedBlamePackedSlice{{PackedBase: 1}},
		0,
	)
	binary.LittleEndian.PutUint16(
		raw[2:4],
		schedBlameExtraBitmapU64Count+1,
	)
	record := new(schedBlameRecord)

	assert.EqualError(
		t,
		decodeSchedBlameRecord(raw, record),
		fmt.Sprintf(
			"incompatible sched-blame extra bitmap u64 count: got %d want %d",
			schedBlameExtraBitmapU64Count+1,
			schedBlameExtraBitmapU64Count,
		),
	)
}

func TestDecodeSchedBlameRejectsUnexpectedSize(t *testing.T) {
	for _, size := range []int{0, 1} {
		t.Run(fmt.Sprintf("size_%d", size), func(t *testing.T) {
			record := new(schedBlameRecord)
			assert.EqualError(
				t,
				decodeSchedBlameRecord(make([]byte, size), record),
				fmt.Sprintf(
					"unexpected sched-blame record size: %d",
					size,
				),
			)
		})
	}

	raw := encodeSchedBlameBatchForTest(
		[]schedBlamePackedSlice{{PackedBase: 1}},
		8,
	)
	record := new(schedBlameRecord)
	assert.EqualError(
		t,
		decodeSchedBlameRecord(raw, record),
		fmt.Sprintf(
			"unexpected sched-blame batch size: count=1 size=%d",
			len(raw),
		),
	)
}

func TestSchedBlameExternalAttributionAndScaling(t *testing.T) {
	const (
		targetCgid      = 0x100
		competitorCgid  = 0x200
		targetCSSID     = 65
		competitorCSSID = 1057
	)
	state := newSchedBlameStateForTest(90, targetCgid)
	state.handleIdentity(&schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: targetCSSID,
		Cgid:  targetCgid,
	})
	first := packSchedBlameSliceForTest(
		1_000,
		1<<schedBlameHighlightIndex,
		competitorCSSID,
	)
	second := packSchedBlameSliceForTest(
		500,
		1<<schedBlameHighlightIndex,
		competitorCSSID,
	)

	// Cross-CPU delivery may expose a slice before its identity record.
	state.handlePackedSlice(&first)
	state.handlePackedSlice(&second)
	chargeIndex := schedBlameExternalChargeIndex(
		competitorCSSID,
		schedBlameHighlightIndex,
	)
	assert.Zero(t, state.externalChargeNsMatrix[chargeIndex])

	state.handleIdentity(&schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: competitorCSSID,
		Cgid:  competitorCgid,
	})
	state.handlePackedSlice(&first)
	state.handlePackedSlice(&second)
	assert.Equal(t, uint64(15_000), state.externalChargeNsMatrix[chargeIndex])
	assert.Equal(t, uint64(15_000),
		state.externalContentionNsByTarget[schedBlameHighlightIndex])
}

func TestSchedBlameAttributesOneSliceToMultipleTargets(t *testing.T) {
	state := newSchedBlameState(0)
	var targets [schedBlameMaxTargets]schedBlameTarget
	targets[0] = schedBlameTarget{cgid: 100, containerID: "target-a"}
	targets[7] = schedBlameTarget{cgid: 200, containerID: "target-b"}
	state.setTargets(&targets)
	state.handleIdentity(&schedBlameIdentity{
		CSSID: 33,
		Cgid:  300,
	})

	packedSlice := packSchedBlameSliceForTest(
		1_000,
		(1<<0)|(1<<7)|(1<<12),
		33,
	)
	state.handlePackedSlice(&packedSlice)

	assert.Equal(t, uint64(1_000),
		state.externalContentionNsByTarget[0])
	assert.Equal(t, uint64(1_000),
		state.externalContentionNsByTarget[7])
	assert.Zero(t, state.externalContentionNsByTarget[12])
}

func TestSchedBlameTargetBitmap(t *testing.T) {
	var bitmap schedBlameTargetBitmap
	bitmap.set(0)
	bitmap.set(schedBlameBaseBitmapBits - 1)

	assert.True(t, bitmap.contains(0))
	assert.True(t, bitmap.contains(schedBlameBaseBitmapBits-1))
	assert.False(t, bitmap.contains(-1))
	assert.False(t, bitmap.contains(schedBlameMaxTargets))
	assert.Equal(t, 2, bitmap.count())

	if schedBlameExtraBitmapU64Count != 0 {
		extendedTarget := schedBlameBaseBitmapBits
		bitmap.set(extendedTarget)
		assert.True(t, bitmap.contains(extendedTarget))
		assert.Equal(t, 3, bitmap.count())
	}
}

func TestSchedBlameAttributesExtendedWaitingTarget(t *testing.T) {
	if schedBlameExtraBitmapU64Count == 0 {
		t.Skip("build has no extended target bitmap")
	}

	state := newSchedBlameState(0)
	const competitorCSSID = 33
	state.handleIdentity(&schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: competitorCSSID,
		Cgid:  300,
	})
	targetIndex := schedBlameMaxTargets - 1
	state.targets[targetIndex] = schedBlameTarget{
		cgid:        400,
		containerID: "extended-target",
	}
	state.activeTargetBitmap.set(targetIndex)

	packedSlice := packSchedBlameSliceForTest(1_000, 0, competitorCSSID)
	extendedIndex := targetIndex - schedBlameBaseBitmapBits
	packedSlice.BitmapExtra[extendedIndex/64] = 1 << (extendedIndex % 64)
	state.handlePackedSlice(&packedSlice)

	assert.Equal(t, uint64(1_000),
		state.externalContentionNsByTarget[targetIndex])
	chargeIndex := schedBlameExternalChargeIndex(
		competitorCSSID,
		targetIndex,
	)
	assert.Equal(t, uint64(1_000),
		state.externalChargeNsMatrix[chargeIndex])
}

func TestSchedBlameReusedTargetSlotClearsAccumulatedState(t *testing.T) {
	state := newSchedBlameState(0)
	var targets [schedBlameMaxTargets]schedBlameTarget
	targets[4] = schedBlameTarget{cgid: 100, containerID: "old"}
	state.setTargets(&targets)
	state.runtimeNsByTarget[4] = 10
	state.internalContentionNsByTarget[4] = 20
	state.externalContentionNsByTarget[4] = 30
	state.throttledTimeNsByTarget[4] = 40
	state.externalContentionRatioHistory[4].add(0.5)
	state.externalChargeNsMatrix[schedBlameExternalChargeIndex(7, 4)] = 50

	targets[4] = schedBlameTarget{cgid: 200, containerID: "new"}
	state.setTargets(&targets)

	assert.Zero(t, state.runtimeNsByTarget[4])
	assert.Zero(t, state.internalContentionNsByTarget[4])
	assert.Zero(t, state.externalContentionNsByTarget[4])
	assert.Zero(t, state.throttledTimeNsByTarget[4])
	assert.Zero(t, state.externalContentionRatioHistory[4].count)
	assert.Zero(t,
		state.externalChargeNsMatrix[schedBlameExternalChargeIndex(7, 4)])
}

func TestSchedBlameAccountsInternalContention(t *testing.T) {
	const (
		targetCgid  = 0x100
		targetCSSID = 65
	)
	state := newSchedBlameStateForTest(0, targetCgid)
	state.handleIdentity(&schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: targetCSSID,
		Cgid:  targetCgid,
	})
	packedSlice := packSchedBlameSliceForTest(
		1_000,
		1<<schedBlameHighlightIndex,
		targetCSSID,
	)

	state.handlePackedSlice(&packedSlice)
	chargeIndex := schedBlameExternalChargeIndex(
		targetCSSID,
		schedBlameHighlightIndex,
	)
	assert.Zero(t, state.externalChargeNsMatrix[chargeIndex])
	assert.Equal(t, uint64(1_000),
		state.runtimeNsByTarget[schedBlameHighlightIndex])
	assert.Equal(t, uint64(1_000),
		state.internalContentionNsByTarget[schedBlameHighlightIndex])
}

func TestSchedBlameAccountsRuntimeWithoutWaitingTarget(t *testing.T) {
	const (
		targetCgid  = 0x100
		targetCSSID = 65
	)
	state := newSchedBlameStateForTest(0, targetCgid)
	state.handleIdentity(&schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: targetCSSID,
		Cgid:  targetCgid,
	})
	packedSlice := packSchedBlameSliceForTest(
		2_000,
		0,
		targetCSSID,
	)

	state.handlePackedSlice(&packedSlice)
	assert.Equal(t, uint64(2_000),
		state.runtimeNsByTarget[schedBlameHighlightIndex])
	assert.Zero(t,
		state.internalContentionNsByTarget[schedBlameHighlightIndex])
}

func TestSchedBlameAccountsThrottleByDenseTarget(t *testing.T) {
	state := newSchedBlameState(0)
	var targets [schedBlameMaxTargets]schedBlameTarget
	targets[7] = schedBlameTarget{cgid: 100, containerID: "target"}
	state.setTargets(&targets)

	state.handleThrottle(&schedBlameThrottle{
		DenseID:    7,
		DurationNs: 123_456,
	})
	state.handleThrottle(&schedBlameThrottle{
		DenseID:    6,
		DurationNs: 1,
	})
	state.handleThrottle(&schedBlameThrottle{
		DenseID:    schedBlameMaxTargets,
		DurationNs: 1,
	})

	assert.Equal(t, uint64(123_456), state.throttledTimeNsByTarget[7])
	assert.Zero(t, state.throttledTimeNsByTarget[6])
	assert.Equal(t, uint64(1), state.invalidThrottleDenseIDs)
}

func TestSchedBlameEvaluatesExternalRatioAgainstPreviousHistory(t *testing.T) {
	oldConfig := configSnapshot()
	t.Cleanup(func() { Set(oldConfig) })
	config := &Config{}
	config.SchedBlame.ExternalAnomalyK = defaultSchedBlameExternalAnomalyK
	Set(config)

	const (
		targetCgid      = 0x100
		competitorCgid  = 0x200
		targetCSSID     = 65
		competitorCSSID = 1057
	)
	state := newSchedBlameStateForTest(0, targetCgid)
	state.handleIdentity(&schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: targetCSSID,
		Cgid:  targetCgid,
	})
	state.handleIdentity(&schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: competitorCSSID,
		Cgid:  competitorCgid,
	})

	for range schedBlameExternalMinimumSamples {
		state.externalContentionRatioHistory[schedBlameHighlightIndex].add(0.1)
	}

	chargeIndex := schedBlameExternalChargeIndex(
		competitorCSSID,
		schedBlameHighlightIndex,
	)
	state.runtimeNsByTarget[schedBlameHighlightIndex] = 1_000
	state.externalContentionNsByTarget[schedBlameHighlightIndex] = 1_000
	state.externalChargeNsMatrix[chargeIndex] = 1_000
	externalEvents := state.evaluateExternalContentionRatios()

	require.Len(t, externalEvents, 1)
	assert.Equal(t, 0.5,
		externalEvents[0].currentExternalContentionRatio)
	assert.InDelta(t, 0.1, externalEvents[0].historicalP99, 1e-9)
	assert.Equal(t, defaultSchedBlameExternalAnomalyK,
		externalEvents[0].externalAnomalyK)
	assert.InDelta(t, 0.1*defaultSchedBlameExternalAnomalyK,
		externalEvents[0].externalContentionRatioThreshold, 1e-9)
	assert.Equal(t, uint64(1_000), externalEvents[0].targetRuntimeNs)
	assert.Zero(t, externalEvents[0].internalContentionNs)
	assert.Equal(t, uint64(1_000), externalEvents[0].externalContentionNs)
	assert.Zero(t, externalEvents[0].throttledTimeNs)
	assert.Equal(t, uint64(1_000),
		externalEvents[0].estimatedTotalWaitNs)
	assert.Equal(t, uint64(2_000),
		externalEvents[0].estimatedTotalDemandNs)
	require.Len(t, externalEvents[0].externalCompetitors, 1)
	assert.Equal(t, uint64(competitorCgid),
		externalEvents[0].externalCompetitors[0].cgid)
	assert.Equal(t, uint64(1_000),
		externalEvents[0].externalCompetitors[0].chargeNs)
	assert.Zero(t, state.runtimeNsByTarget[schedBlameHighlightIndex])
	assert.Zero(t,
		state.internalContentionNsByTarget[schedBlameHighlightIndex])
	assert.Zero(t,
		state.externalContentionNsByTarget[schedBlameHighlightIndex])
	assert.Zero(t,
		state.throttledTimeNsByTarget[schedBlameHighlightIndex])
	assert.Zero(t, state.externalChargeNsMatrix[chargeIndex])
}

func TestSchedBlameExternalRatioIncludesAllDemandComponents(t *testing.T) {
	state := newSchedBlameStateForTest(0, 0x100)
	history := &state.externalContentionRatioHistory[schedBlameHighlightIndex]
	for range schedBlameExternalMinimumSamples {
		history.add(0.1)
	}
	state.runtimeNsByTarget[schedBlameHighlightIndex] = 1_000
	state.internalContentionNsByTarget[schedBlameHighlightIndex] = 2_000
	state.externalContentionNsByTarget[schedBlameHighlightIndex] = 3_000
	state.throttledTimeNsByTarget[schedBlameHighlightIndex] = 4_000

	events := state.evaluateExternalContentionRatios()

	require.Len(t, events, 1)
	assert.Equal(t, 0.3, events[0].currentExternalContentionRatio)
	assert.Equal(t, uint64(2_000), events[0].internalContentionNs)
	assert.Equal(t, uint64(4_000), events[0].throttledTimeNs)
	assert.Equal(t, uint64(9_000),
		events[0].estimatedTotalWaitNs)
	assert.Equal(t, uint64(10_000), events[0].estimatedTotalDemandNs)
}

func TestSchedBlameThrottleOnlyIntervalIsValid(t *testing.T) {
	state := newSchedBlameStateForTest(0, 0x100)
	state.throttledTimeNsByTarget[schedBlameHighlightIndex] = 1_000

	assert.Empty(t, state.evaluateExternalContentionRatios())
	history := &state.externalContentionRatioHistory[schedBlameHighlightIndex]
	assert.Equal(t, 1, history.count)
	assert.Zero(t, history.values[0])
	assert.Zero(t,
		state.throttledTimeNsByTarget[schedBlameHighlightIndex])
}

func TestSchedBlameSkipsExternalEventWhenP99IsZero(t *testing.T) {
	const (
		targetCgid  = 0x100
		targetCSSID = 65
	)
	state := newSchedBlameStateForTest(0, targetCgid)
	state.handleIdentity(&schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: targetCSSID,
		Cgid:  targetCgid,
	})
	history := &state.externalContentionRatioHistory[schedBlameHighlightIndex]
	for range schedBlameExternalMinimumSamples {
		history.add(0)
	}
	state.runtimeNsByTarget[schedBlameHighlightIndex] = 1_000
	state.externalContentionNsByTarget[schedBlameHighlightIndex] = 1_000

	assert.Empty(t, state.evaluateExternalContentionRatios())
	assert.Equal(t, schedBlameExternalMinimumSamples+1,
		history.count)
}

func TestSchedBlameEvaluatesEveryActiveTarget(t *testing.T) {
	state := newSchedBlameState(0)
	var targets [schedBlameMaxTargets]schedBlameTarget
	targets[0] = schedBlameTarget{cgid: 100, containerID: "target-a"}
	targets[7] = schedBlameTarget{cgid: 200, containerID: "target-b"}
	state.setTargets(&targets)

	for _, targetIndex := range []int{0, 7} {
		for range schedBlameExternalMinimumSamples {
			state.externalContentionRatioHistory[targetIndex].add(0.1)
		}
		state.runtimeNsByTarget[targetIndex] = 1_000
		state.externalContentionNsByTarget[targetIndex] = 1_000
	}

	events := state.evaluateExternalContentionRatios()
	require.Len(t, events, 2)
	assert.Equal(t, uint8(0), events[0].targetIndex)
	assert.Equal(t, uint8(7), events[1].targetIndex)
	assert.Zero(t, state.runtimeNsByTarget[0])
	assert.Zero(t, state.runtimeNsByTarget[7])
}

func TestSchedBlameDoesNotRecordInactiveRatio(t *testing.T) {
	const (
		targetCgid  = 0x100
		targetCSSID = 65
	)
	state := newSchedBlameStateForTest(0, targetCgid)
	state.handleIdentity(&schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: targetCSSID,
		Cgid:  targetCgid,
	})

	assert.Empty(t, state.evaluateExternalContentionRatios())
	history := &state.externalContentionRatioHistory[schedBlameHighlightIndex]
	assert.Zero(t, history.count)
	assert.Zero(t, history.next)
}

func TestSchedBlameExternalDataJSONNames(t *testing.T) {
	raw, err := json.Marshal(&SchedBlameExternalData{})
	require.NoError(t, err)

	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))
	assert.Contains(t, fields, "current_external_contention_percent")
	assert.Contains(t, fields, "historical_p99_percent")
	assert.Contains(t, fields, "external_anomaly_k")
	assert.Contains(t, fields, "external_contention_threshold_percent")
	assert.Contains(t, fields, "slice_drop_percent")
	assert.Contains(t, fields, "internal_contention_ns")
	assert.Contains(t, fields, "throttled_time_ns")
	assert.Contains(t, fields, "estimated_total_wait_ns")
	assert.Contains(t, fields, "estimated_total_demand_ns")
	assert.Contains(t, fields, "top_external_competitors")
	assert.NotContains(t, fields, "estimated_hierarchical_wait_ns")
	assert.NotContains(t, fields, "event_drop_percent")
	assert.NotContains(t, fields, "current_external_contention_ratio")
	assert.NotContains(t, fields, "historical_p99")
	assert.NotContains(t, fields, "external_contention_ratio_threshold")
}

func TestSchedBlameExternalDataUsesPercentages(t *testing.T) {
	state := newSchedBlameState(0)
	targets := [schedBlameMaxTargets]schedBlameTarget{}
	targets[schedBlameHighlightIndex] = schedBlameTarget{
		cgid:        0x100,
		containerID: "0123456789abcdef",
		name:        "workload-a",
		cgroupPath:  "/target",
	}
	state.setTargets(&targets)
	uploader := &schedBlameUploader{
		queue: make(chan schedBlameUpload, 1),
	}

	assert.True(t, state.enqueueExternalEvent(
		&schedBlameExternalEvent{
			targetIndex:                      schedBlameHighlightIndex,
			currentExternalContentionRatio:   0.75,
			historicalP99:                    0.4,
			externalAnomalyK:                 1.25,
			externalContentionRatioThreshold: 0.5,
			targetRuntimeNs:                  100,
			internalContentionNs:             200,
			externalContentionNs:             300,
			throttledTimeNs:                  400,
			estimatedTotalWaitNs:             900,
			estimatedTotalDemandNs:           1_000,
		},
		time.Now(),
		uploader,
		nil,
		nil,
		0,
	))

	upload := <-uploader.queue
	data, ok := upload.request.TracerData.(*SchedBlameExternalData)
	require.True(t, ok)
	assert.Equal(t, 75.0, data.CurrentExternalContentionPercent)
	assert.Equal(t, 40.0, data.HistoricalP99Percent)
	assert.Equal(t, 1.25, data.ExternalAnomalyK)
	assert.Equal(t, 50.0, data.ExternalContentionThresholdPercent)
	assert.Equal(t, defaultSchedBlameSliceDropPercent, data.SliceDropPercent)
	assert.Equal(t, uint64(100), data.TargetRuntimeNs)
	assert.Equal(t, uint64(200), data.InternalContentionNs)
	assert.Equal(t, uint64(300), data.ExternalContentionNs)
	assert.Equal(t, uint64(400), data.ThrottledTimeNs)
	assert.Equal(t, uint64(900), data.EstimatedTotalWaitNs)
	assert.Equal(t, uint64(1_000), data.EstimatedTotalDemandNs)
}

func TestSchedBlameExternalContentionRatioHistoryP99(t *testing.T) {
	var history schedBlameExternalContentionRatioHistory
	for index := 1; index < schedBlameExternalMinimumSamples; index++ {
		history.add(float64(index) / 100)
	}
	var scratch [schedBlameExternalContentionRatioHistorySize]float64

	_, ready := history.p99(&scratch)
	assert.False(t, ready)

	history.add(0.60)
	p99, ready := history.p99(&scratch)
	assert.True(t, ready)
	assert.InDelta(t, 0.60, p99, 1e-9)

	for index := 61; index <= 100; index++ {
		history.add(float64(index) / 100)
	}
	p99, ready = history.p99(&scratch)
	assert.True(t, ready)
	assert.InDelta(t, 0.99, p99, 1e-9)
}

func TestSchedBlameTopExternalCompetitorsAggregateAndSort(t *testing.T) {
	state := newSchedBlameStateForTest(0, 0x100)
	state.cssKnown[1] = true
	state.cssKnown[2] = true
	state.cssKnown[3] = true
	state.cssCgids[1] = 0x300
	state.cssCgids[2] = 0x200
	state.cssCgids[3] = 0x300
	state.externalChargeNsMatrix[schedBlameExternalChargeIndex(1, 0)] = 30
	state.externalChargeNsMatrix[schedBlameExternalChargeIndex(2, 0)] = 50
	state.externalChargeNsMatrix[schedBlameExternalChargeIndex(3, 0)] = 20

	competitors := state.topExternalCompetitors(0)

	require.Len(t, competitors, 2)
	assert.Equal(t, uint64(0x200), competitors[0].cgid)
	assert.Equal(t, uint64(50), competitors[0].chargeNs)
	assert.Equal(t, uint64(0x300), competitors[1].cgid)
	assert.Equal(t, uint64(50), competitors[1].chargeNs)
}

func TestSchedBlameHighlightContainerMatches(t *testing.T) {
	const containerID = "0123456789abcdef"
	const hostname = "workload-a"

	assert.True(t, schedBlameHighlightContainerMatches(
		containerID, containerID, hostname))
	assert.True(t, schedBlameHighlightContainerMatches(
		"0123456789ab", containerID, hostname))
	assert.True(t, schedBlameHighlightContainerMatches(
		hostname, containerID, hostname))
	assert.False(t, schedBlameHighlightContainerMatches(
		"", containerID, hostname))
	assert.False(t, schedBlameHighlightContainerMatches(
		"other", containerID, hostname))
}

func TestSchedBlameShortIDMatchesCrictl(t *testing.T) {
	assert.Equal(t, "0123456789abc",
		schedBlameShortID("0123456789abcdef"))
	assert.Equal(t, "short-id", schedBlameShortID("short-id"))
}

func TestSchedBlameFindHighlightTarget(t *testing.T) {
	containers := map[string]*pod.Container{
		"0123456789abcdef": {
			Hostname:   "workload-a",
			CgroupPath: "/workload-a",
			CgroupCss:  map[string]uint64{"cpu": 101},
		},
		"012345ffffffffff": {
			Hostname:   "workload-b",
			CgroupPath: "/workload-b",
			CgroupCss:  map[string]uint64{"cpu": 102},
		},
	}

	target, exists, err := schedBlameFindHighlightTarget(containers, "", true)
	require.NoError(t, err)
	assert.False(t, exists)
	assert.False(t, target.active())

	_, _, err = schedBlameFindHighlightTarget(containers, "missing", true)
	assert.EqualError(t, err,
		`sched-blame: highlighted container "missing" not found`)

	_, _, err = schedBlameFindHighlightTarget(containers, "012345", true)
	assert.EqualError(t, err,
		`sched-blame: highlighted container "012345" matched 2 containers`)

	target, exists, err = schedBlameFindHighlightTarget(
		containers,
		"workload-a",
		true,
	)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, uint64(101), target.cgid)
	assert.Equal(t, "0123456789abcdef", target.containerID)
	assert.Equal(t, "workload-a", target.name)
	assert.Equal(t, "/workload-a", target.cgroupPath)
}

func TestSchedBlameSelectTargetsUsesSlotZeroWithoutHighlight(t *testing.T) {
	oldConfig := configSnapshot()
	t.Cleanup(func() { Set(oldConfig) })
	config := &Config{}
	config.SchedBlame.TargetContainerScope = schedBlameTargetContainerScopeAll
	Set(config)
	runtimeConfig := schedBlameRuntimeConfigSnapshot()

	containers := map[string]*pod.Container{
		"survivor-id": {
			Hostname:  "survivor",
			CgroupCss: map[string]uint64{subsystem.SubsystemCPU: 101},
		},
		"new-id": {
			Hostname:  "new",
			CgroupCss: map[string]uint64{subsystem.SubsystemCPU: 102},
		},
	}
	var oldTargets [schedBlameMaxTargets]schedBlameTarget
	oldTargets[0], _ = schedBlameTargetFromContainer(
		"survivor-id",
		containers["survivor-id"],
	)

	targets, err := schedBlameSelectTargets(
		containers,
		&oldTargets,
		&runtimeConfig,
		true,
		func(_ int, _ func(int, int)) error { return nil },
	)
	require.NoError(t, err)
	assert.Equal(t, "survivor-id", targets[0].containerID)
	assert.Equal(t, "new-id", targets[1].containerID)
	targetBitmap := schedBlameTargetsBitmap(&targets)
	assert.Equal(t, 2, targetBitmap.count())
}

func TestSchedBlameSelectTargetsReservesMissingHighlightSlot(t *testing.T) {
	oldConfig := configSnapshot()
	t.Cleanup(func() { Set(oldConfig) })
	config := &Config{}
	config.SchedBlame.HighlightContainer = "missing"
	config.SchedBlame.TargetContainerScope = schedBlameTargetContainerScopeAll
	Set(config)
	runtimeConfig := schedBlameRuntimeConfigSnapshot()

	targets, err := schedBlameSelectTargets(
		map[string]*pod.Container{
			"candidate-id": {
				Hostname:  "candidate",
				CgroupCss: map[string]uint64{subsystem.SubsystemCPU: 101},
			},
		},
		&[schedBlameMaxTargets]schedBlameTarget{},
		&runtimeConfig,
		false,
		func(_ int, _ func(int, int)) error { return nil },
	)
	require.NoError(t, err)
	assert.False(t, targets[0].active())
	assert.Equal(t, "candidate-id", targets[1].containerID)
}

func TestSchedBlameSelectTargetsPreservesSurvivingSlots(t *testing.T) {
	oldConfig := configSnapshot()
	t.Cleanup(func() { Set(oldConfig) })
	config := &Config{}
	config.SchedBlame.HighlightContainer = "highlight"
	config.SchedBlame.TargetContainerScope = schedBlameTargetContainerScopeNormal
	Set(config)
	runtimeConfig := schedBlameRuntimeConfigSnapshot()

	containers := map[string]*pod.Container{
		"highlight-id": {
			Hostname:  "highlight",
			Type:      pod.ContainerTypeDaemonSet,
			CgroupCss: map[string]uint64{subsystem.SubsystemCPU: 100},
		},
		"survivor-id": {
			Hostname:  "survivor",
			Type:      pod.ContainerTypeNormal,
			CgroupCss: map[string]uint64{subsystem.SubsystemCPU: 101},
		},
		"new-id": {
			Hostname:  "new",
			Type:      pod.ContainerTypeNormal,
			CgroupCss: map[string]uint64{subsystem.SubsystemCPU: 102},
		},
		"excluded-id": {
			Hostname:  "excluded",
			Type:      pod.ContainerTypeDaemonSet,
			CgroupCss: map[string]uint64{subsystem.SubsystemCPU: 103},
		},
	}
	var oldTargets [schedBlameMaxTargets]schedBlameTarget
	oldTargets[3], _ = schedBlameTargetFromContainer(
		"survivor-id",
		containers["survivor-id"],
	)

	targets, err := schedBlameSelectTargets(
		containers,
		&oldTargets,
		&runtimeConfig,
		true,
		func(_ int, _ func(int, int)) error { return nil },
	)
	require.NoError(t, err)
	assert.Equal(t, "highlight-id",
		targets[schedBlameHighlightIndex].containerID)
	assert.Equal(t, "new-id", targets[1].containerID)
	assert.Equal(t, "survivor-id", targets[3].containerID)
	for _, target := range targets {
		assert.NotEqual(t, "excluded-id", target.containerID)
	}
}

func TestSchedBlameSelectTargetsRejectsInvalidScope(t *testing.T) {
	oldConfig := configSnapshot()
	t.Cleanup(func() { Set(oldConfig) })
	config := &Config{}
	config.SchedBlame.HighlightContainer = "highlight"
	config.SchedBlame.TargetContainerScope = "invalid"
	Set(config)
	runtimeConfig := schedBlameRuntimeConfigSnapshot()

	_, err := schedBlameSelectTargets(
		map[string]*pod.Container{
			"highlight-id": {
				Hostname:  "highlight",
				CgroupCss: map[string]uint64{subsystem.SubsystemCPU: 100},
			},
		},
		&[schedBlameMaxTargets]schedBlameTarget{},
		&runtimeConfig,
		true,
		func(_ int, _ func(int, int)) error { return nil },
	)
	assert.EqualError(t, err,
		`sched-blame: invalid TargetContainerScope "invalid"`)
}

func TestSchedBlameSelectTargetsCapsTargetCount(t *testing.T) {
	oldConfig := configSnapshot()
	t.Cleanup(func() { Set(oldConfig) })
	config := &Config{}
	config.SchedBlame.HighlightContainer = "highlight"
	config.SchedBlame.TargetContainerScope = schedBlameTargetContainerScopeAll
	Set(config)
	runtimeConfig := schedBlameRuntimeConfigSnapshot()

	containers := make(map[string]*pod.Container)
	for index := 0; index < schedBlameMaxTargets+10; index++ {
		containerID := fmt.Sprintf("container-%02d", index)
		if index == 0 {
			containerID = "highlight"
		}
		containers[containerID] = &pod.Container{
			Hostname:  containerID,
			CgroupCss: map[string]uint64{subsystem.SubsystemCPU: uint64(index + 1)},
		}
	}

	targets, err := schedBlameSelectTargets(
		containers,
		&[schedBlameMaxTargets]schedBlameTarget{},
		&runtimeConfig,
		true,
		func(_ int, _ func(int, int)) error { return nil },
	)
	require.NoError(t, err)
	targetBitmap := schedBlameTargetsBitmap(&targets)
	assert.Equal(t, schedBlameMaxTargets, targetBitmap.count())
	assert.Equal(t, "highlight",
		targets[schedBlameHighlightIndex].containerID)
}

func TestPublishSchedBlameTargets(t *testing.T) {
	const (
		targetMapID = 37
		dataMapID   = 38
	)
	bpf := &schedBlamePublishBPFForTest{
		targetMapID: targetMapID,
		dataMapID:   dataMapID,
		dataValue:   make([]byte, 4),
		existing: []ibpf.MapItem{{
			Key: make([]byte, 8),
		}},
		writes: make(map[uint32][]ibpf.MapItem),
	}
	state := newSchedBlameState(0)
	var targets [schedBlameMaxTargets]schedBlameTarget
	targets[0] = schedBlameTarget{cgid: 100, containerID: "highlight"}
	targets[5] = schedBlameTarget{cgid: 200, containerID: "random"}

	require.NoError(t, publishSchedBlameTargets(bpf, state, &targets))
	assert.Equal(t, uint32(1), state.targetCSSIDEpoch)
	assert.True(t, state.activeTargetBitmap.contains(0))
	assert.True(t, state.activeTargetBitmap.contains(5))
	assert.Equal(t, 2, state.activeTargetBitmap.count())
	require.Len(t, bpf.deleted, 1)
	require.Len(t, bpf.writes[targetMapID], 2)
	assert.Equal(t, []byte{0}, bpf.writes[targetMapID][0].Value)
	assert.Equal(t, []byte{5}, bpf.writes[targetMapID][1].Value)
	require.Len(t, bpf.writes[dataMapID], 1)
	assert.Equal(t, uint32(1), binary.LittleEndian.Uint32(
		bpf.writes[dataMapID][0].Value,
	))
}

func TestSchedBlamePublicationFailureInvalidatesAttribution(t *testing.T) {
	const (
		targetMapID = 37
		dataMapID   = 38
	)
	publishErr := errors.New("publish epoch")
	bpf := &schedBlamePublishBPFForTest{
		targetMapID: targetMapID,
		dataMapID:   dataMapID,
		dataValue:   make([]byte, 4),
		writes:      make(map[uint32][]ibpf.MapItem),
		writeErrors: map[uint32]error{dataMapID: publishErr},
	}
	state := newSchedBlameState(0)
	runner := &schedBlameRunner{bpf: bpf, state: state}
	var targets [schedBlameMaxTargets]schedBlameTarget
	targets[0] = schedBlameTarget{cgid: 100, containerID: "target"}

	require.ErrorIs(t, runner.publishTargets(&targets), publishErr)
	assert.True(t, runner.attributionInvalid)
	assert.Equal(t, "target", runner.state.targets[0].containerID)
	assert.Zero(t, runner.state.targetCSSIDEpoch)
}

func TestSchedBlameExternalRatioDebugWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug", "ratios.csv")
	writer, err := newSchedBlameExternalRatioDebugWriter(path)
	require.NoError(t, err)
	require.NotNil(t, writer)
	timestamp := time.Date(2026, 7, 21, 1, 2, 3, 4, time.UTC)
	require.NoError(t, writer.writeBatch(
		schedBlameExternalRatioDebugBatch{
			timestamp: timestamp,
			samples: []schedBlameExternalRatioDebugSample{
				{
					targetIndex:          3,
					cssID:                10,
					cssKnown:             true,
					containerID:          "container-a",
					containerName:        "workload-a",
					sampleValid:          true,
					currentRatio:         0.25,
					historicalP99:        0.1,
					externalAnomalyK:     2,
					threshold:            0.2,
					historySamples:       60,
					ready:                true,
					targetRuntimeNs:      300,
					internalContentionNs: 50,
					externalContentionNs: 100,
					throttledTimeNs:      25,
					emitted:              true,
					irmasSample: schedBlameIrmasSample{
						valid:              true,
						waitrate:           0.4375,
						hierarchyWaitDelta: 100,
						innerWaitDelta:     20,
						throttleWaitDelta:  10,
						externalWaitDelta:  70,
						cpuUsageDelta:      60,
						totalDemandDelta:   160,
					},
				},
				{
					targetIndex:      4,
					cssID:            11,
					containerID:      "container-b",
					containerName:    "workload-b",
					externalAnomalyK: 2,
					historySamples:   12,
				},
			},
		},
	))
	require.NoError(t, writer.close())

	file, err := os.Open(path)
	require.NoError(t, err)
	defer func() {
		assert.NoError(t, file.Close())
	}()
	records, err := csv.NewReader(file).ReadAll()
	require.NoError(t, err)
	require.Len(t, records, 3)
	assert.Equal(t, schedBlameRatioDebugHeader, records[0])
	assert.Equal(t, "2026-07-21T01:02:03.000000004+00:00", records[1][0])
	assert.Equal(t, "3", records[1][1])
	assert.Equal(t, "10", records[1][4])
	assert.Equal(t, "true", records[1][5])
	assert.Equal(t, "43.75", records[1][6])
	assert.Equal(t, "25", records[1][7])
	assert.Equal(t, "10", records[1][8])
	assert.Equal(t, "2", records[1][9])
	assert.Equal(t, "20", records[1][10])
	assert.Equal(t, "60", records[1][11])
	assert.Equal(t, "true", records[1][12])
	assert.Equal(t, "true", records[1][13])
	assert.Equal(t, "300", records[1][14])
	assert.Equal(t, "50", records[1][15])
	assert.Equal(t, "100", records[1][16])
	assert.Equal(t, "25", records[1][17])
	assert.Equal(t, "175", records[1][18])
	assert.Equal(t, "475", records[1][19])
	assert.Equal(t, "false", records[2][5])
	assert.Empty(t, records[2][6])
	assert.Empty(t, records[2][7])
	assert.Empty(t, records[2][8])
	assert.Equal(t, "2", records[2][9])
	assert.Empty(t, records[2][10])
}

func TestSchedBlameRatioDebugWriteFailureStopsEvaluation(t *testing.T) {
	writer, err := newSchedBlameExternalRatioDebugWriter(
		filepath.Join(t.TempDir(), "ratios.csv"),
	)
	require.NoError(t, err)
	require.NoError(t, writer.file.Close())

	state := newSchedBlameState(0)
	var targets [schedBlameMaxTargets]schedBlameTarget
	targets[0] = schedBlameTarget{cgid: 100, containerID: "target"}
	state.setTargets(&targets)
	runner := &schedBlameRunner{
		config:           schedBlameRuntimeConfig{externalAnomalyK: 1},
		state:            state,
		ratioDebugWriter: writer,
		irmasSampler:     newSchedBlameIrmasSampler(),
	}

	assert.Error(t, runner.evaluate(time.Now()))
}

func TestSchedBlameUploadQueueIsBounded(t *testing.T) {
	uploader := &schedBlameUploader{
		queue: make(chan schedBlameUpload, 1),
	}

	assert.True(t, uploader.enqueue(schedBlameUpload{}))
	assert.False(t, uploader.enqueue(schedBlameUpload{}))
	length, capacity, dropped := uploader.stats()
	assert.Equal(t, 1, length)
	assert.Equal(t, 1, capacity)
	assert.Equal(t, uint64(1), dropped)
}

func TestSchedBlameUploaderUsesEventWriter(t *testing.T) {
	written := make(chan *tracing.WriteRequest, 1)
	uploader := newSchedBlameUploader(func(
		_ context.Context,
		request *tracing.WriteRequest,
	) error {
		written <- request
		return nil
	})
	request := &tracing.WriteRequest{TracerName: "sched-blame-external"}

	assert.True(t, uploader.enqueue(schedBlameUpload{request: request}))
	uploader.close()

	select {
	case got := <-written:
		assert.Same(t, request, got)
	default:
		t.Fatal("event writer was not called")
	}
}

func TestSchedBlameUploaderCancelsBlockedWriteAfterDrainTimeout(t *testing.T) {
	started := make(chan struct{})
	uploader := newSchedBlameUploader(func(
		ctx context.Context,
		_ *tracing.WriteRequest,
	) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	uploader.drainTimeout = 10 * time.Millisecond
	require.True(t, uploader.enqueue(schedBlameUpload{
		request: &tracing.WriteRequest{},
	}))
	<-started

	uploader.close()

	_, _, dropped := uploader.stats()
	assert.Equal(t, uint64(1), dropped)
}

func TestSchedBlameSliceKeepThreshold(t *testing.T) {
	threshold, keepAll := schedBlameSliceKeepThreshold(0)
	assert.Zero(t, threshold)
	assert.Equal(t, uint32(1), keepAll)

	threshold, keepAll = schedBlameSliceKeepThreshold(90)
	assert.Equal(t, uint32(429496729), threshold)
	assert.Zero(t, keepAll)

	threshold, keepAll = schedBlameSliceKeepThreshold(100)
	assert.Zero(t, threshold)
	assert.Zero(t, keepAll)
}

func TestSchedBlameQueuePressure(t *testing.T) {
	pressureReader := &schedBlamePressureReaderForTest{
		bufferSize: 4096,
	}
	reader := &schedBlamePerfReader{
		reader:  pressureReader,
		records: make(chan *schedBlameRecord, 8),
	}

	reader.observePerfDepth(3072)
	reader.observeQueueDepth(2)
	reader.observeQueueDepth(6)
	reader.observeQueueDepth(4)
	for range 3 {
		reader.records <- &schedBlameRecord{}
	}

	stats := reader.takePressureStats()
	assert.True(t, stats.perfSupported)
	assert.Equal(t, uint64(3072), stats.perfPeakBytes)
	assert.Equal(t, uint64(4096), stats.perfCapacityBytes)
	assert.Equal(t, 75.0, schedBlamePressurePercent(
		stats.perfPeakBytes,
		stats.perfCapacityBytes,
	))
	assert.Equal(t, uint64(6), stats.queuePeakRecords)
	assert.Equal(t, uint64(8), stats.queueCapacity)
	assert.Equal(t, 75.0, schedBlamePressurePercent(
		stats.queuePeakRecords,
		stats.queueCapacity,
	))

	stats = reader.takePressureStats()
	assert.Zero(t, stats.perfPeakBytes)
	assert.Equal(t, uint64(4096), stats.perfCapacityBytes)
	assert.Equal(t, uint64(3), stats.queuePeakRecords)
	assert.Equal(t, uint64(8), stats.queueCapacity)
}

func TestSchedBlameSliceBatchFillPercent(t *testing.T) {
	assert.Zero(t, schedBlameSliceBatchFillPercent(0, 0, 128))
	assert.Equal(t, 50.0, schedBlameSliceBatchFillPercent(128, 2, 128))
	assert.Equal(t, 100.0, schedBlameSliceBatchFillPercent(256, 2, 128))
}

func TestDrainSchedBlameTailBatches(t *testing.T) {
	first := make([]byte, schedBlameBatchValueSize)
	second := make([]byte, schedBlameBatchValueSize)
	want := []schedBlamePackedSlice{
		packSchedBlameSliceForTest(10, 1, 100),
		packSchedBlameSliceForTest(20, 1, 200),
	}
	copy(second, encodeSchedBlameBatchForTest(want, 0))
	values := first
	values = append(values, second...)
	bpf := &schedBlameTargetBPFForTest{
		mapID:     17,
		readValue: values,
	}

	var got []schedBlamePackedSlice
	require.NoError(t, drainSchedBlameTailBatches(
		bpf,
		func(batch *schedBlameBatch) {
			for index := uint32(0); index < batch.count; index++ {
				got = append(got, batch.packedSlices[index])
			}
		},
	))
	assert.Equal(t, "slice_batches_percpu", bpf.mapName)
	assert.Equal(t, want, got)
}

func TestValidateSchedBlameBatchMapValueSize(t *testing.T) {
	bpf := &schedBlameTargetBPFForTest{
		info: &ibpf.Info{MapsInfo: []ibpf.MapInfo{{
			Name:      "slice_batches_percpu",
			ValueSize: schedBlameBatchValueSize,
		}}},
	}
	require.NoError(t, validateSchedBlameBatchMapValueSize(bpf))

	bpf.info.MapsInfo[0].ValueSize--
	assert.EqualError(
		t,
		validateSchedBlameBatchMapValueSize(bpf),
		fmt.Sprintf(
			"sched-blame: incompatible slice_batches_percpu value size: got %d want %d",
			schedBlameBatchValueSize-1,
			schedBlameBatchValueSize,
		),
	)
}

func TestSchedBlamePerfReaderUsesConfiguredQueueCapacity(t *testing.T) {
	old := configSnapshot()
	t.Cleanup(func() { Set(old) })

	config := &Config{}
	config.SchedBlame.PerfEventQueueRecords = 1024
	Set(config)

	reader := newSchedBlamePerfReader(
		context.Background(),
		&schedBlameExitReaderForTest{},
		1024,
	)
	defer reader.cancel()

	assert.Equal(t, 1024, cap(reader.records))
	assert.Equal(t, 1024, cap(reader.freeRecords))
}

func TestSchedBlamePerfReaderDrainIsBounded(t *testing.T) {
	reader := &schedBlamePerfReader{
		records: make(chan *schedBlameRecord, 1),
	}
	reader.records <- new(schedBlameRecord)

	handled := 0
	require.NoError(t, reader.Drain(func(*schedBlameRecord) {
		handled++
		reader.records <- new(schedBlameRecord)
	}))

	assert.Equal(t, 1, handled)
	assert.Len(t, reader.records, 1)
}

func TestSchedBlamePerfReaderFinalDrainPreservesQueuedRecords(t *testing.T) {
	rawReader := &schedBlameFlushReaderForTest{
		records: make(chan []byte, 3),
	}
	rawReader.records <- encodeSchedBlameIdentityForTest(schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: 1,
		Cgid:  100,
	})
	rawReader.records <- encodeSchedBlameIdentityForTest(schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: 2,
		Cgid:  200,
	})
	reader := newSchedBlamePerfReader(context.Background(), rawReader, 3)
	require.NoError(t, reader.Flush())

	var cgids []uint64
	require.NoError(t, reader.FinalDrain(func(record *schedBlameRecord) {
		cgids = append(cgids, record.identity.Cgid)
	}))
	require.NoError(t, reader.Close())
	assert.Equal(t, []uint64{100, 200}, cgids)
}

func TestSchedBlamePerfReaderCloseThenFinalDrain(t *testing.T) {
	rawReader := &schedBlameFlushReaderForTest{
		records: make(chan []byte, 1),
	}
	reader := newSchedBlamePerfReader(context.Background(), rawReader, 1)

	require.NoError(t, reader.Close())
	require.NoError(t, reader.FinalDrain(func(*schedBlameRecord) {}))
}

func TestSchedBlameFinalDrainDiscardsInvalidAttribution(t *testing.T) {
	rawReader := &schedBlameFlushReaderForTest{
		records: make(chan []byte, 2),
	}
	rawReader.records <- encodeSchedBlameIdentityForTest(schedBlameIdentity{
		Magic: schedBlameIdentityMagic,
		CSSID: 1,
		Cgid:  100,
	})
	state := newSchedBlameState(0)
	runner := &schedBlameRunner{
		reader:             newSchedBlamePerfReader(context.Background(), rawReader, 2),
		state:              state,
		detach:             func() error { return nil },
		attributionInvalid: true,
	}

	runner.finalDrain()
	assert.False(t, state.cssKnown[1])
}

func TestSchedBlameFinalEvaluationBypassesPeriodicGate(t *testing.T) {
	state := newSchedBlameState(0)
	var targets [schedBlameMaxTargets]schedBlameTarget
	targets[1] = schedBlameTarget{
		cgid:        100,
		containerID: "target",
	}
	state.setTargets(&targets)
	state.runtimeNsByTarget[1] = 100

	now := time.Now()
	runner := &schedBlameRunner{
		state:          state,
		irmasSampler:   newSchedBlameIrmasSampler(),
		lastEvaluation: now,
	}
	require.NoError(t, runner.maybeEvaluate(now.Add(time.Millisecond)))
	assert.Equal(t, uint64(100), state.runtimeNsByTarget[1])

	require.NoError(t, runner.evaluate(now.Add(time.Millisecond)))
	assert.Zero(t, state.runtimeNsByTarget[1])
	assert.Equal(t, 1, state.externalContentionRatioHistory[1].count)
}

type schedBlamePressureReaderForTest struct {
	ibpf.PerfEventRawReader
	bufferSize int
}

func (reader *schedBlamePressureReaderForTest) PerCPUBufferSize() int {
	return reader.bufferSize
}

type schedBlameExitReaderForTest struct {
	ibpf.PerfEventRawReader
}

type schedBlameFlushReaderForTest struct {
	ibpf.PerfEventRawReader
	records   chan []byte
	flushOnce sync.Once
}

func (reader *schedBlameFlushReaderForTest) ReadRawInto(
	record *ibpf.PerfEventRawRecord,
) error {
	raw := <-reader.records
	if raw == nil {
		return ibpf.ErrPerfEventReaderFlushed
	}
	record.RawSample = raw
	return nil
}

func (reader *schedBlameFlushReaderForTest) Flush() error {
	reader.flushOnce.Do(func() {
		reader.records <- nil
	})
	return nil
}

func (reader *schedBlameFlushReaderForTest) Close() error {
	return reader.Flush()
}

func (*schedBlameExitReaderForTest) ReadRawInto(*ibpf.PerfEventRawRecord) error {
	return types.ErrExitByCancelCtx
}

type schedBlameTargetBPFForTest struct {
	ibpf.BPF
	mapID        uint32
	mapName      string
	info         *ibpf.Info
	infoErr      error
	writeErr     error
	readValue    []byte
	readErr      error
	dumpMapErr   error
	dumpMapItems []ibpf.MapItem
}

func (bpf *schedBlameTargetBPFForTest) Info() (*ibpf.Info, error) {
	return bpf.info, bpf.infoErr
}

type schedBlamePublishBPFForTest struct {
	ibpf.BPF
	targetMapID uint32
	dataMapID   uint32
	dataValue   []byte
	existing    []ibpf.MapItem
	deleted     [][]byte
	writes      map[uint32][]ibpf.MapItem
	writeErrors map[uint32]error
}

func (bpf *schedBlamePublishBPFForTest) MapIDByName(name string) uint32 {
	if name == "target_cgid_to_dense" {
		return bpf.targetMapID
	}
	return 0
}

func (bpf *schedBlamePublishBPFForTest) Info() (*ibpf.Info, error) {
	return &ibpf.Info{
		MapsInfo: []ibpf.MapInfo{{
			ID:   bpf.dataMapID,
			Name: "data",
		}},
	}, nil
}

func (bpf *schedBlamePublishBPFForTest) DumpMap(
	_ uint32,
) ([]ibpf.MapItem, error) {
	return bpf.existing, nil
}

func (bpf *schedBlamePublishBPFForTest) DeleteMapItems(
	_ uint32,
	keys [][]byte,
) error {
	bpf.deleted = keys
	return nil
}

func (bpf *schedBlamePublishBPFForTest) ReadMap(
	_ uint32,
	_ []byte,
) ([]byte, error) {
	return append([]byte(nil), bpf.dataValue...), nil
}

func (bpf *schedBlamePublishBPFForTest) WriteMapItems(
	mapID uint32,
	items []ibpf.MapItem,
) error {
	if err := bpf.writeErrors[mapID]; err != nil {
		return err
	}
	bpf.writes[mapID] = append(bpf.writes[mapID], items...)
	return nil
}

func (bpf *schedBlameTargetBPFForTest) MapIDByName(name string) uint32 {
	bpf.mapName = name
	return bpf.mapID
}

func (bpf *schedBlameTargetBPFForTest) WriteMapItems(
	_ uint32,
	_ []ibpf.MapItem,
) error {
	return bpf.writeErr
}

func (bpf *schedBlameTargetBPFForTest) ReadMap(
	_ uint32,
	_ []byte,
) ([]byte, error) {
	return bpf.readValue, bpf.readErr
}

func (bpf *schedBlameTargetBPFForTest) DumpMap(
	_ uint32,
) ([]ibpf.MapItem, error) {
	if bpf.dumpMapItems != nil || bpf.dumpMapErr != nil {
		return bpf.dumpMapItems, bpf.dumpMapErr
	}
	return []ibpf.MapItem{{
		Key:   []byte{0, 0, 0, 0},
		Value: bpf.readValue,
	}}, nil
}

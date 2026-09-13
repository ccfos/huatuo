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
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"math/bits"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	cebpf "github.com/cilium/ebpf"
	"github.com/sirupsen/logrus"

	ibpf "github.com/ccfos/huatuo/internal/bpf"
	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/cgroups/subsystem"
	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/tracing"
	"github.com/ccfos/huatuo/pkg/types"
)

func init() {
	tracing.RegisterEventTracing("sched-blame", newSchedBlameTracing)
}

func newSchedBlameTracing() (*tracing.EventTracingAttr, error) {
	return &tracing.EventTracingAttr{
		TracingData: &schedBlameTracing{
			eventWriter: tracing.SaveContext,
		},
		Interval: 300,
		Flag:     tracing.FlagTracing,
	}, nil
}

type schedBlameEventWriter func(
	ctx context.Context,
	request *tracing.WriteRequest,
) error

//go:generate $BPF_COMPILE $BPF_INCLUDE -s $BPF_DIR/sched_blame.c -o $BPF_DIR/sched_blame.o

const (
	schedBlameCSSIDBits      = 12
	schedBlameMaxCSSIDs      = 1 << schedBlameCSSIDBits
	schedBlameCSSIDMask      = schedBlameMaxCSSIDs - 1
	schedBlameBaseBitmapBits = 20
	// Keep this equal to EXTRA_BITMAP_U64_COUNT in bpf/sched_blame.c.
	schedBlameExtraBitmapU64Count = 1
	schedBlameMaxTargets          = schedBlameBaseBitmapBits +
		64*schedBlameExtraBitmapU64Count
	schedBlameBaseBitmapMask = (1 << schedBlameBaseBitmapBits) - 1
	schedBlameHighlightIndex = 0
	schedBlameNonTarget      = 0xff
	schedBlameSliceSize      = 8 *
		(1 + schedBlameExtraBitmapU64Count)
	schedBlameBatchHeaderSize = 8
	schedBlameMaxBatchSlices  = 128
	schedBlameBatchValueSize  = schedBlameBatchHeaderSize + schedBlameMaxBatchSlices*schedBlameSliceSize
	schedBlameIdentitySize    = 16
	schedBlameThrottleSize    = 8
	schedBlameIdentityMagic   = 0x1111
	schedBlameThrottleMagic   = 0x2222
	schedBlameBatchMagic      = 0x3333
	schedBlameMaxPairs        = schedBlameMaxCSSIDs *
		schedBlameMaxTargets
	schedBlamePerfRawSizeFieldSize = 4
	schedBlamePerfSampleAlignment  = 8
	// Dense IDs and the non-target sentinel share one byte in the BPF ABI.
	_ uint8 = schedBlameMaxTargets
)

const (
	schedBlameEvaluationInterval  = time.Second
	schedBlameStatusInterval      = 60 * time.Second
	schedBlameDebugInterval       = time.Second
	schedBlameUploadQueueCapacity = 1024
	schedBlameUploadDrainTimeout  = 5 * time.Second
	schedBlameTargetSyncInterval  = 60 * time.Second
	schedBlameTargetSyncRetry     = 5 * time.Second
)

type schedBlameIdentity struct {
	Magic uint16
	CSSID uint16
	_     uint32
	Cgid  uint64
}

var (
	_ [abi.SchedBlameIdentityEventSize]byte   = [schedBlameIdentitySize]byte{}
	_ [abi.SchedBlameThrottleEventSize]byte   = [schedBlameThrottleSize]byte{}
	_ [abi.SchedBlamePackedSliceSize]byte     = [schedBlameSliceSize]byte{}
	_ [abi.SchedBlameSliceBatchEventSize]byte = [schedBlameBatchValueSize]byte{}
)

type schedBlameThrottle struct {
	Magic      uint16
	DenseID    uint16
	DurationNs uint32
}

type schedBlameTargetBitmap struct {
	base  uint32
	extra [schedBlameExtraBitmapU64Count]uint64
}

type schedBlamePackedSlice struct {
	PackedBase  uint64
	BitmapExtra [schedBlameExtraBitmapU64Count]uint64
}

type schedBlameBatch struct {
	count        uint32
	packedSlices [schedBlameMaxBatchSlices]schedBlamePackedSlice
}

type schedBlameRecordKind uint8

const (
	schedBlameRecordInvalid schedBlameRecordKind = iota
	schedBlameRecordIdentity
	schedBlameRecordThrottle
	schedBlameRecordBatch
)

type schedBlameRecord struct {
	identity schedBlameIdentity
	throttle schedBlameThrottle
	batch    schedBlameBatch
	kind     schedBlameRecordKind
}

func schedBlameBatchCapacity(count uint32) int {
	return 1 << bits.Len32(count-1)
}

func schedBlamePerfRawSampleSize(payloadSize int) int {
	// PERF_SAMPLE_RAW aligns its u32 size field plus payload to u64. The
	// perf reader omits that field but returns the trailing kernel padding.
	alignedSize := payloadSize + schedBlamePerfRawSizeFieldSize +
		schedBlamePerfSampleAlignment - 1
	alignedSize &^= schedBlamePerfSampleAlignment - 1
	return alignedSize - schedBlamePerfRawSizeFieldSize
}

func decodeSchedBlamePackedSlices(
	raw []byte,
	count uint32,
	packedSlices *[schedBlameMaxBatchSlices]schedBlamePackedSlice,
) {
	offset := schedBlameBatchHeaderSize
	for index := range int(count) {
		packedSlice := &packedSlices[index]
		packedSlice.PackedBase = binary.LittleEndian.Uint64(raw[offset:])
		offset += 8
		for extraIndex := range &packedSlice.BitmapExtra {
			packedSlice.BitmapExtra[extraIndex] = binary.LittleEndian.Uint64(raw[offset:])
			offset += 8
		}
	}
}

func decodeSchedBlameIdentity(
	raw []byte,
	identity *schedBlameIdentity,
) error {
	if len(raw) != schedBlamePerfRawSampleSize(schedBlameIdentitySize) {
		return fmt.Errorf("unexpected sched-blame record size: %d", len(raw))
	}
	decoded := schedBlameIdentity{
		Magic: binary.LittleEndian.Uint16(raw[:2]),
		CSSID: binary.LittleEndian.Uint16(raw[2:4]),
		Cgid:  binary.LittleEndian.Uint64(raw[8:16]),
	}
	if decoded.CSSID >= schedBlameMaxCSSIDs || decoded.Cgid == 0 {
		return errors.New("invalid sched-blame identity record")
	}
	*identity = decoded
	return nil
}

func decodeSchedBlameThrottle(
	raw []byte,
	throttle *schedBlameThrottle,
) error {
	if len(raw) != schedBlamePerfRawSampleSize(schedBlameThrottleSize) {
		return fmt.Errorf(
			"unexpected sched-blame throttle size: %d",
			len(raw),
		)
	}
	decoded := schedBlameThrottle{
		Magic:      binary.LittleEndian.Uint16(raw[:2]),
		DenseID:    binary.LittleEndian.Uint16(raw[2:4]),
		DurationNs: binary.LittleEndian.Uint32(raw[4:8]),
	}
	if decoded.DenseID >= schedBlameMaxTargets {
		return fmt.Errorf(
			"invalid sched-blame throttle dense ID: %d",
			decoded.DenseID,
		)
	}
	*throttle = decoded
	return nil
}

func validateSchedBlameBatchHeader(raw []byte) (uint32, error) {
	if len(raw) < schedBlameBatchHeaderSize {
		return 0, fmt.Errorf("unexpected sched-blame record size: %d", len(raw))
	}
	magic := binary.LittleEndian.Uint16(raw[:2])
	if magic != schedBlameBatchMagic {
		return 0, fmt.Errorf("invalid sched-blame batch magic: %#x", magic)
	}
	extraBitmapU64Count := binary.LittleEndian.Uint16(raw[2:4])
	if extraBitmapU64Count != schedBlameExtraBitmapU64Count {
		return 0, fmt.Errorf(
			"incompatible sched-blame extra bitmap u64 count: got %d want %d",
			extraBitmapU64Count,
			schedBlameExtraBitmapU64Count,
		)
	}
	count := binary.LittleEndian.Uint32(raw[4:8])
	if count == 0 || count > schedBlameMaxBatchSlices {
		return 0, fmt.Errorf("invalid sched-blame batch count: %d", count)
	}
	return count, nil
}

func decodeSchedBlameBatch(
	raw []byte,
	batch *schedBlameBatch,
	mapValue bool,
) error {
	count, err := validateSchedBlameBatchHeader(raw)
	if err != nil {
		return err
	}

	payloadSize := schedBlameBatchValueSize
	if !mapValue {
		capacity := schedBlameBatchCapacity(count)
		payloadSize = schedBlameBatchHeaderSize +
			capacity*schedBlameSliceSize
	}
	expectedSize := payloadSize
	if !mapValue {
		expectedSize = schedBlamePerfRawSampleSize(payloadSize)
	}
	if len(raw) != expectedSize {
		return fmt.Errorf(
			"unexpected sched-blame batch size: count=%d size=%d",
			count,
			len(raw),
		)
	}

	batch.count = count
	decodeSchedBlamePackedSlices(raw, count, &batch.packedSlices)
	return nil
}

func decodeSchedBlameRecord(raw []byte, record *schedBlameRecord) error {
	record.kind = schedBlameRecordInvalid
	if len(raw) < 2 {
		return fmt.Errorf("unexpected sched-blame record size: %d", len(raw))
	}

	switch magic := binary.LittleEndian.Uint16(raw[:2]); magic {
	case schedBlameIdentityMagic:
		if err := decodeSchedBlameIdentity(raw, &record.identity); err != nil {
			return err
		}
		record.kind = schedBlameRecordIdentity
	case schedBlameThrottleMagic:
		if err := decodeSchedBlameThrottle(raw, &record.throttle); err != nil {
			return err
		}
		record.kind = schedBlameRecordThrottle
	case schedBlameBatchMagic:
		if err := decodeSchedBlameBatch(raw, &record.batch, false); err != nil {
			return err
		}
		record.kind = schedBlameRecordBatch
	default:
		return fmt.Errorf("invalid sched-blame record magic: %#x", magic)
	}
	return nil
}

// Keep this table behind a pointer. Its size is
// schedBlameMaxCSSIDs * schedBlameMaxTargets * 8 bytes.
type schedBlameExternalChargeMatrix [schedBlameMaxPairs]uint64

type schedBlameTarget struct {
	cgid        uint64
	containerID string
	name        string
	cgroupPath  string
	cssID       uint16
	cssKnown    bool
}

func (target schedBlameTarget) active() bool {
	return target.cgid != 0 && target.containerID != ""
}

func (bitmap *schedBlameTargetBitmap) set(targetIndex int) {
	if targetIndex < 0 || targetIndex >= schedBlameMaxTargets {
		return
	}
	if targetIndex < schedBlameBaseBitmapBits {
		bitmap.base |= 1 << targetIndex
		return
	}
	extendedIndex := targetIndex - schedBlameBaseBitmapBits
	bitmap.extra[extendedIndex/64] |= 1 << (extendedIndex % 64)
}

func (bitmap *schedBlameTargetBitmap) contains(targetIndex int) bool {
	if targetIndex < 0 || targetIndex >= schedBlameMaxTargets {
		return false
	}
	if targetIndex < schedBlameBaseBitmapBits {
		return bitmap.base&(1<<targetIndex) != 0
	}
	extendedIndex := targetIndex - schedBlameBaseBitmapBits
	return bitmap.extra[extendedIndex/64]&(1<<(extendedIndex%64)) != 0
}

func (bitmap *schedBlameTargetBitmap) count() int {
	count := bits.OnesCount32(bitmap.base & schedBlameBaseBitmapMask)
	for _, value := range &bitmap.extra {
		count += bits.OnesCount64(value)
	}
	return count
}

type schedBlameState struct {
	cssCgids                       [schedBlameMaxCSSIDs]uint64
	cssKnown                       [schedBlameMaxCSSIDs]bool
	targetDenseByCSSID             [schedBlameMaxCSSIDs]uint8
	targets                        [schedBlameMaxTargets]schedBlameTarget
	activeTargetBitmap             schedBlameTargetBitmap
	targetCSSIDEpoch               uint32
	externalChargeNsMatrix         *schedBlameExternalChargeMatrix
	runtimeNsByTarget              [schedBlameMaxTargets]uint64
	internalContentionNsByTarget   [schedBlameMaxTargets]uint64
	externalContentionNsByTarget   [schedBlameMaxTargets]uint64
	throttledTimeNsByTarget        [schedBlameMaxTargets]uint64
	externalContentionRatioHistory [schedBlameMaxTargets]schedBlameExternalContentionRatioHistory
	sampleScale                    float64
	invalidThrottleDenseIDs        uint64
}

func newSchedBlameState(
	sliceDropPercent uint32,
) *schedBlameState {
	keepPercent := uint32(100) - sliceDropPercent
	scale := float64(0)
	if keepPercent != 0 {
		scale = 100 / float64(keepPercent)
	}
	state := &schedBlameState{
		externalChargeNsMatrix: new(schedBlameExternalChargeMatrix),
		sampleScale:            scale,
	}
	for cssID := range state.targetDenseByCSSID {
		state.targetDenseByCSSID[cssID] = schedBlameNonTarget
	}
	return state
}

func (state *schedBlameState) activeTargetCount() int {
	return state.activeTargetBitmap.count()
}

func schedBlameSameTargetOwner(left, right schedBlameTarget) bool {
	if !left.active() || !right.active() {
		return !left.active() && !right.active()
	}
	return left.containerID == right.containerID && left.cgid == right.cgid
}

func schedBlameSameTargetAssignments(
	left, right *[schedBlameMaxTargets]schedBlameTarget,
) bool {
	for targetIndex := range left {
		if !schedBlameSameTargetOwner(left[targetIndex], right[targetIndex]) {
			return false
		}
	}
	return true
}

func schedBlameTargetsBitmap(
	targets *[schedBlameMaxTargets]schedBlameTarget,
) schedBlameTargetBitmap {
	var bitmap schedBlameTargetBitmap
	for targetIndex, target := range targets {
		if target.active() {
			bitmap.set(targetIndex)
		}
	}
	return bitmap
}

func (state *schedBlameState) clearReusedTargetSlot(targetIndex int) {
	state.runtimeNsByTarget[targetIndex] = 0
	state.internalContentionNsByTarget[targetIndex] = 0
	state.externalContentionNsByTarget[targetIndex] = 0
	state.throttledTimeNsByTarget[targetIndex] = 0
	state.externalContentionRatioHistory[targetIndex] = schedBlameExternalContentionRatioHistory{}
	for competitorCSSID := 0; competitorCSSID < schedBlameMaxCSSIDs; competitorCSSID++ {
		state.externalChargeNsMatrix[schedBlameExternalChargeIndex(
			uint16(competitorCSSID),
			targetIndex,
		)] = 0
	}
}

func (state *schedBlameState) rebuildTargetDenseByCSSID() {
	for cssID := range state.targetDenseByCSSID {
		state.targetDenseByCSSID[cssID] = schedBlameNonTarget
	}
	denseIDByCgid := make(map[uint64]uint8, state.activeTargetCount())
	for targetIndex := range state.targets {
		state.targets[targetIndex].cssKnown = false
		if state.targets[targetIndex].active() {
			denseIDByCgid[state.targets[targetIndex].cgid] = uint8(targetIndex)
		}
	}
	for cssID, known := range &state.cssKnown {
		if !known {
			continue
		}
		targetIndex, exists := denseIDByCgid[state.cssCgids[cssID]]
		if !exists {
			continue
		}
		state.targetDenseByCSSID[cssID] = targetIndex
		target := &state.targets[targetIndex]
		target.cssID = uint16(cssID)
		target.cssKnown = true
	}
}

func (state *schedBlameState) setTargets(
	targets *[schedBlameMaxTargets]schedBlameTarget,
) {
	for targetIndex := range targets {
		if !schedBlameSameTargetOwner(
			state.targets[targetIndex],
			targets[targetIndex],
		) {
			state.clearReusedTargetSlot(targetIndex)
		}
	}
	state.targets = *targets
	state.activeTargetBitmap = schedBlameTargetsBitmap(targets)
	state.rebuildTargetDenseByCSSID()
}

func (state *schedBlameState) handleIdentity(
	identity *schedBlameIdentity,
) {
	if identity.CSSID >= schedBlameMaxCSSIDs || identity.Cgid == 0 {
		return
	}
	state.cssCgids[identity.CSSID] = identity.Cgid
	state.cssKnown[identity.CSSID] = true
	oldTargetIndex := state.targetDenseByCSSID[identity.CSSID]
	if oldTargetIndex != schedBlameNonTarget &&
		oldTargetIndex < schedBlameMaxTargets {
		target := &state.targets[oldTargetIndex]
		if target.cssKnown && target.cssID == identity.CSSID {
			target.cssKnown = false
		}
	}
	state.targetDenseByCSSID[identity.CSSID] = schedBlameNonTarget
	for targetIndex := range state.targets {
		target := &state.targets[targetIndex]
		if !target.active() || identity.Cgid != target.cgid {
			continue
		}
		state.targetDenseByCSSID[identity.CSSID] = uint8(targetIndex)
		target.cssID = identity.CSSID
		target.cssKnown = true
		break
	}
}

func schedBlameUnpackSliceBase(
	packed *schedBlamePackedSlice,
) (uint16, uint64) {
	cssID := uint16(packed.PackedBase & uint64(schedBlameCSSIDMask))
	duration := packed.PackedBase >> 32
	return cssID, duration
}

func schedBlameSliceHasTarget(
	packed *schedBlamePackedSlice,
	targetIndex int,
) bool {
	if targetIndex < 0 || targetIndex >= schedBlameMaxTargets {
		return false
	}
	if targetIndex < schedBlameBaseBitmapBits {
		mask := uint64(1) << (schedBlameCSSIDBits + targetIndex)
		return packed.PackedBase&mask != 0
	}
	extendedIndex := targetIndex - schedBlameBaseBitmapBits
	mask := uint64(1) << (extendedIndex % 64)
	return packed.BitmapExtra[extendedIndex/64]&mask != 0
}

func schedBlameExternalChargeIndex(
	competitorCSSID uint16,
	targetIndex int,
) int {
	return int(competitorCSSID)*schedBlameMaxTargets + targetIndex
}

func (state *schedBlameState) scaledDuration(duration uint64) uint64 {
	if duration == 0 || state.sampleScale == 0 {
		return 0
	}
	if state.sampleScale == 1 {
		return duration
	}
	scaled := float64(duration) * state.sampleScale
	if scaled >= float64(math.MaxUint64) {
		return math.MaxUint64
	}
	return uint64(scaled)
}

func (state *schedBlameState) handlePackedSlice(
	packedSlice *schedBlamePackedSlice,
) {
	competitorCSSID, duration := schedBlameUnpackSliceBase(packedSlice)
	if !state.cssKnown[competitorCSSID] {
		return
	}
	estimatedDuration := state.scaledDuration(duration)
	if estimatedDuration == 0 {
		return
	}
	competitorDenseID := state.targetDenseByCSSID[competitorCSSID]
	if competitorDenseID != schedBlameNonTarget &&
		competitorDenseID < schedBlameMaxTargets {
		competitorTargetIndex := int(competitorDenseID)
		state.runtimeNsByTarget[competitorTargetIndex] += estimatedDuration
		if schedBlameSliceHasTarget(packedSlice, competitorTargetIndex) {
			state.internalContentionNsByTarget[competitorTargetIndex] += estimatedDuration
		}
	}

	waitingTargets := uint32(
		(packedSlice.PackedBase >> schedBlameCSSIDBits) &
			uint64(schedBlameBaseBitmapMask),
	)
	for waitingTargets != 0 {
		targetIndex := bits.TrailingZeros32(waitingTargets)
		if targetIndex != int(competitorDenseID) &&
			state.activeTargetBitmap.contains(targetIndex) {
			chargeIndex := schedBlameExternalChargeIndex(
				competitorCSSID,
				targetIndex,
			)
			state.externalChargeNsMatrix[chargeIndex] += estimatedDuration
			state.externalContentionNsByTarget[targetIndex] += estimatedDuration
		}
		waitingTargets &= waitingTargets - 1
	}
	for extraIndex, extraTargets := range &packedSlice.BitmapExtra {
		for extraTargets != 0 {
			bit := bits.TrailingZeros64(extraTargets)
			targetIndex := schedBlameBaseBitmapBits + 64*extraIndex + bit
			if targetIndex != int(competitorDenseID) &&
				state.activeTargetBitmap.contains(targetIndex) {
				chargeIndex := schedBlameExternalChargeIndex(
					competitorCSSID,
					targetIndex,
				)
				state.externalChargeNsMatrix[chargeIndex] += estimatedDuration
				state.externalContentionNsByTarget[targetIndex] += estimatedDuration
			}
			extraTargets &= extraTargets - 1
		}
	}
}

func (state *schedBlameState) handleThrottle(
	throttle *schedBlameThrottle,
) {
	if throttle.DenseID >= schedBlameMaxTargets {
		state.invalidThrottleDenseIDs++
		return
	}
	targetIndex := int(throttle.DenseID)
	if !state.activeTargetBitmap.contains(targetIndex) {
		return
	}
	state.throttledTimeNsByTarget[targetIndex] += uint64(throttle.DurationNs)
}

func (state *schedBlameState) handleBatch(batch *schedBlameBatch) {
	remaining := batch.count
	for index := range &batch.packedSlices {
		if remaining == 0 {
			return
		}
		state.handlePackedSlice(&batch.packedSlices[index])
		remaining--
	}
}

func (state *schedBlameState) handleRecord(record *schedBlameRecord) {
	switch record.kind {
	case schedBlameRecordIdentity:
		state.handleIdentity(&record.identity)
	case schedBlameRecordThrottle:
		state.handleThrottle(&record.throttle)
	case schedBlameRecordBatch:
		state.handleBatch(&record.batch)
	}
}

func schedBlameTargetFromContainer(
	containerID string,
	container *pod.Container,
) (schedBlameTarget, bool) {
	if container == nil {
		return schedBlameTarget{}, false
	}
	cgid, exists := container.CgroupCss[subsystem.SubsystemCPU]
	if !exists || cgid == 0 {
		return schedBlameTarget{}, false
	}
	return schedBlameTarget{
		cgid:        cgid,
		containerID: containerID,
		name:        container.Hostname,
		cgroupPath:  container.CgroupPath,
	}, true
}

func schedBlameFindHighlightTarget(
	containers map[string]*pod.Container,
	selector string,
	requireConfigured bool,
) (schedBlameTarget, bool, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return schedBlameTarget{}, false, nil
	}

	var selectedContainerID string
	var selectedContainer *pod.Container
	matches := 0
	for containerID, container := range containers {
		if container == nil ||
			!schedBlameHighlightContainerMatches(
				selector,
				containerID,
				container.Hostname,
			) {
			continue
		}
		matches++
		selectedContainerID = containerID
		selectedContainer = container
	}

	switch matches {
	case 0:
		if requireConfigured {
			return schedBlameTarget{}, false, fmt.Errorf(
				"sched-blame: highlighted container %q not found",
				selector,
			)
		}
		return schedBlameTarget{}, false, nil
	case 1:
		target, valid := schedBlameTargetFromContainer(
			selectedContainerID,
			selectedContainer,
		)
		if !valid {
			if !requireConfigured {
				return schedBlameTarget{}, false, nil
			}
			return schedBlameTarget{}, false, fmt.Errorf(
				"sched-blame: highlighted container %q has no CPU cgroup CSS",
				selector,
			)
		}
		return target, true, nil
	default:
		return schedBlameTarget{}, false, fmt.Errorf(
			"sched-blame: highlighted container %q matched %d containers",
			selector,
			matches,
		)
	}
}

func schedBlameTargetScopeAllowed(
	container *pod.Container,
	config *schedBlameRuntimeConfig,
) (bool, error) {
	switch config.targetContainerScope {
	case schedBlameTargetContainerScopeAll:
		return true, nil
	case schedBlameTargetContainerScopeNormal:
		return container.Type.IsNormal(), nil
	default:
		return false, fmt.Errorf(
			"sched-blame: invalid TargetContainerScope %q",
			config.targetContainerScope,
		)
	}
}

func schedBlameTargetQosAllowed(
	container *pod.Container,
	allowed []string,
) bool {
	if len(allowed) == 0 {
		return true
	}
	return slices.Contains(
		allowed,
		strings.ToLower(container.Qos.String()),
	)
}

func schedBlameSelectTargets(
	containers map[string]*pod.Container,
	oldTargets *[schedBlameMaxTargets]schedBlameTarget,
	config *schedBlameRuntimeConfig,
	requireConfiguredHighlight bool,
	shuffle func(int, func(int, int)) error,
) ([schedBlameMaxTargets]schedBlameTarget, error) {
	var selected [schedBlameMaxTargets]schedBlameTarget
	highlightConfigured := config.highlightConfigured()
	highlight, highlightExists, err := schedBlameFindHighlightTarget(
		containers,
		config.highlightContainer,
		requireConfiguredHighlight,
	)
	if err != nil {
		return selected, err
	}
	if highlightExists {
		selected[schedBlameHighlightIndex] = highlight
	}
	firstCandidateIndex := 0
	if highlightConfigured {
		firstCandidateIndex = schedBlameHighlightIndex + 1
	}

	candidateCPUValid := 0
	scopeAllowed := 0
	buildAllowed := 0
	candidates := make(map[string]schedBlameTarget, len(containers))
	for containerID, container := range containers {
		target, valid := schedBlameTargetFromContainer(containerID, container)
		if !valid {
			continue
		}
		allowed, err := schedBlameTargetScopeAllowed(container, config)
		if err != nil {
			return selected, err
		}
		if highlightExists && containerID == highlight.containerID {
			continue
		}
		candidateCPUValid++
		if !allowed {
			continue
		}
		scopeAllowed++
		if !schedBlameTargetQosAllowed(container, config.targetQos) {
			continue
		}
		buildAllowed++
		candidates[containerID] = target
	}

	for targetIndex := firstCandidateIndex; targetIndex < schedBlameMaxTargets; targetIndex++ {
		oldTarget := oldTargets[targetIndex]
		candidate, exists := candidates[oldTarget.containerID]
		if !exists || !schedBlameSameTargetOwner(oldTarget, candidate) {
			continue
		}
		selected[targetIndex] = candidate
		delete(candidates, oldTarget.containerID)
	}

	randomTargets := make([]schedBlameTarget, 0, len(candidates))
	for _, target := range candidates {
		randomTargets = append(randomTargets, target)
	}
	if err := shuffle(len(randomTargets), func(left, right int) {
		randomTargets[left], randomTargets[right] = randomTargets[right], randomTargets[left]
	}); err != nil {
		return selected, fmt.Errorf(
			"sched-blame: shuffle target candidates: %w",
			err,
		)
	}
	nextRandom := 0
	for targetIndex := firstCandidateIndex; targetIndex < schedBlameMaxTargets &&
		nextRandom < len(randomTargets); targetIndex++ {
		if selected[targetIndex].active() {
			continue
		}
		selected[targetIndex] = randomTargets[nextRandom]
		nextRandom++
	}
	selectedBitmap := schedBlameTargetsBitmap(&selected)
	log.Debugf(
		"sched-blame: target selection discovered=%d highlight_configured=%t highlight_found=%t candidate_cpu_css_valid=%d scope_allowed=%d%s selected=%d",
		len(containers),
		highlightConfigured,
		highlightExists,
		candidateCPUValid,
		scopeAllowed,
		fmt.Sprintf(" qos_allowed=%d", buildAllowed),
		selectedBitmap.count(),
	)
	return selected, nil
}

func schedBlameShuffleTargets(
	length int,
	swap func(int, int),
) error {
	for left := length - 1; left > 0; left-- {
		right, err := cryptorand.Int(
			cryptorand.Reader,
			big.NewInt(int64(left+1)),
		)
		if err != nil {
			return err
		}
		swap(left, int(right.Int64()))
	}
	return nil
}

func resolveSchedBlameTargets(
	oldTargets *[schedBlameMaxTargets]schedBlameTarget,
	config *schedBlameRuntimeConfig,
	requireConfiguredHighlight bool,
) ([schedBlameMaxTargets]schedBlameTarget, error) {
	containers, err := pod.Containers()
	if err != nil {
		return [schedBlameMaxTargets]schedBlameTarget{}, fmt.Errorf(
			"sched-blame: list containers: %w",
			err,
		)
	}
	return schedBlameSelectTargets(
		containers,
		oldTargets,
		config,
		requireConfiguredHighlight,
		schedBlameShuffleTargets,
	)
}

func schedBlameMapID(b ibpf.BPF, name string) (uint32, error) {
	mapID := b.MapIDByName(name)
	if mapID == 0 {
		return 0, fmt.Errorf("sched-blame: %s map not found", name)
	}
	return mapID, nil
}

func validateSchedBlameBatchMapValueSize(b ibpf.BPF) error {
	info, err := b.Info()
	if err != nil {
		return fmt.Errorf("sched-blame: inspect BPF maps: %w", err)
	}
	for _, candidate := range info.MapsInfo {
		if candidate.Name != "slice_batches_percpu" {
			continue
		}
		if candidate.ValueSize != schedBlameBatchValueSize {
			return fmt.Errorf(
				"sched-blame: incompatible slice_batches_percpu value size: got %d want %d",
				candidate.ValueSize,
				schedBlameBatchValueSize,
			)
		}
		return nil
	}
	return errors.New("sched-blame: slice_batches_percpu map not found")
}

func schedBlameDataMapID(b ibpf.BPF) (uint32, error) {
	info, err := b.Info()
	if err != nil {
		return 0, fmt.Errorf("sched-blame: inspect BPF maps: %w", err)
	}
	for _, candidate := range info.MapsInfo {
		if candidate.Name == "data" ||
			candidate.Name == ".data" ||
			candidate.Name == "sched_blame.da" ||
			strings.HasSuffix(candidate.Name, ".data") {
			return candidate.ID, nil
		}
	}
	return 0, errors.New("sched-blame: writable data map not found")
}

func writeSchedBlameTargetEpoch(b ibpf.BPF, epoch uint32) error {
	mapID, err := schedBlameDataMapID(b)
	if err != nil {
		return err
	}
	key := []byte{0, 0, 0, 0}
	value, err := b.ReadMap(mapID, key)
	if err != nil {
		return fmt.Errorf("sched-blame: read writable data map: %w", err)
	}
	if len(value) < 4 {
		return fmt.Errorf(
			"sched-blame: writable data map value is too short: %d",
			len(value),
		)
	}
	binary.LittleEndian.PutUint32(value[:4], epoch)
	if err := b.WriteMapItems(mapID, []ibpf.MapItem{{
		Key:   key,
		Value: value,
	}}); err != nil {
		return fmt.Errorf("sched-blame: publish target CSS-ID epoch: %w", err)
	}
	return nil
}

func publishSchedBlameTargets(
	b ibpf.BPF,
	state *schedBlameState,
	targets *[schedBlameMaxTargets]schedBlameTarget,
) error {
	if state.targetCSSIDEpoch == math.MaxUint32 {
		return errors.New("sched-blame: target CSS-ID epoch exhausted")
	}
	nextEpoch := state.targetCSSIDEpoch + 1

	mapID, err := schedBlameMapID(b, "target_cgid_to_dense")
	if err != nil {
		return err
	}
	existing, err := b.DumpMap(mapID)
	if err != nil {
		return fmt.Errorf(
			"sched-blame: read target assignment map: %w",
			err,
		)
	}
	if len(existing) != 0 {
		keys := make([][]byte, 0, len(existing))
		for _, item := range existing {
			keys = append(keys, item.Key)
		}
		if err := b.DeleteMapItems(mapID, keys); err != nil {
			return fmt.Errorf(
				"sched-blame: clear target assignment map: %w",
				err,
			)
		}
	}

	items := make([]ibpf.MapItem, 0, schedBlameMaxTargets)
	for targetIndex := range targets {
		target := targets[targetIndex]
		if !target.active() {
			continue
		}
		key := make([]byte, 8)
		binary.LittleEndian.PutUint64(key, target.cgid)
		items = append(items, ibpf.MapItem{
			Key:   key,
			Value: []byte{byte(targetIndex)},
		})
	}
	if len(items) != 0 {
		if err := b.WriteMapItems(mapID, items); err != nil {
			return fmt.Errorf(
				"sched-blame: publish target assignment map: %w",
				err,
			)
		}
	}

	oldTargets := state.targets
	state.setTargets(targets)
	if err := writeSchedBlameTargetEpoch(b, nextEpoch); err != nil {
		return err
	}
	state.targetCSSIDEpoch = nextEpoch
	for targetIndex := range targets {
		oldTarget := oldTargets[targetIndex]
		target := targets[targetIndex]
		if schedBlameSameTargetOwner(oldTarget, target) && target.active() {
			log.Infof(
				"sched-blame: retained target dense_id=%d container=%s id=%s",
				targetIndex,
				target.name,
				schedBlameShortID(target.containerID),
			)
			continue
		}
		if oldTarget.active() {
			log.Infof(
				"sched-blame: removed target dense_id=%d container=%s id=%s",
				targetIndex,
				oldTarget.name,
				schedBlameShortID(oldTarget.containerID),
			)
		}
		if target.active() {
			log.Infof(
				"sched-blame: assigned target dense_id=%d container=%s id=%s",
				targetIndex,
				target.name,
				schedBlameShortID(target.containerID),
			)
		}
	}
	return nil
}

type schedBlamePerfReader struct {
	reader            ibpf.PerfEventRawReader
	cancel            context.CancelFunc
	done              chan struct{}
	closeOnce         sync.Once
	closeErr          error
	records           chan *schedBlameRecord
	freeRecords       chan *schedBlameRecord
	lostSamples       atomic.Uint64
	sliceBatchRecords atomic.Uint64
	slices            atomic.Uint64
	queuePeakRecords  atomic.Uint64
	perfPeakBytes     atomic.Uint64
	errMu             sync.Mutex
	err               error
}

func newSchedBlamePerfReader(
	ctx context.Context,
	reader ibpf.PerfEventRawReader,
	queueRecords int,
) *schedBlamePerfReader {
	pumpCtx, cancel := context.WithCancel(ctx)
	result := &schedBlamePerfReader{
		reader:      reader,
		cancel:      cancel,
		done:        make(chan struct{}),
		records:     make(chan *schedBlameRecord, queueRecords),
		freeRecords: make(chan *schedBlameRecord, queueRecords),
	}
	go result.pump(pumpCtx)
	return result
}

func (reader *schedBlamePerfReader) Close() error {
	reader.closeOnce.Do(func() {
		reader.cancel()
		reader.closeErr = reader.reader.Close()
	})
	return reader.closeErr
}

func (reader *schedBlamePerfReader) Flush() error {
	return reader.reader.Flush()
}

func (reader *schedBlamePerfReader) setError(err error) {
	reader.errMu.Lock()
	if reader.err == nil {
		reader.err = err
	}
	reader.errMu.Unlock()
}

func (reader *schedBlamePerfReader) getError() error {
	reader.errMu.Lock()
	defer reader.errMu.Unlock()
	return reader.err
}

func (reader *schedBlamePerfReader) getRecord() *schedBlameRecord {
	select {
	case record := <-reader.freeRecords:
		return record
	default:
		return new(schedBlameRecord)
	}
}

func (reader *schedBlamePerfReader) putRecord(record *schedBlameRecord) {
	record.kind = schedBlameRecordInvalid
	record.batch.count = 0
	select {
	case reader.freeRecords <- record:
	default:
	}
}

func (reader *schedBlamePerfReader) pump(ctx context.Context) {
	defer close(reader.done)
	var rawRecord ibpf.PerfEventRawRecord
	for {
		err := reader.reader.ReadRawInto(&rawRecord)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, types.ErrExitByCancelCtx) ||
				errors.Is(err, ibpf.ErrPerfEventReaderFlushed) {
				return
			}
			reader.setError(err)
			return
		}
		if rawRecord.LostSamples != 0 {
			reader.lostSamples.Add(rawRecord.LostSamples)
			continue
		}
		reader.observePerfDepth(rawRecord.RemainingBytes + rawRecord.PerfRecordSize)
		record := reader.getRecord()
		if err := decodeSchedBlameRecord(rawRecord.RawSample, record); err != nil {
			reader.putRecord(record)
			reader.setError(err)
			return
		}
		batchCount := uint32(0)
		if record.kind == schedBlameRecordBatch {
			batchCount = record.batch.count
		}
		select {
		case reader.records <- record:
			reader.observeQueueDepth(len(reader.records))
			if batchCount != 0 {
				reader.sliceBatchRecords.Add(1)
				reader.slices.Add(uint64(batchCount))
			}
		case <-ctx.Done():
			reader.putRecord(record)
			return
		}
	}
}

func (reader *schedBlamePerfReader) observePerfDepth(depth int) {
	value := uint64(depth)
	for {
		peak := reader.perfPeakBytes.Load()
		if value <= peak || reader.perfPeakBytes.CompareAndSwap(peak, value) {
			return
		}
	}
}

func (reader *schedBlamePerfReader) observeQueueDepth(depth int) {
	value := uint64(depth)
	for {
		peak := reader.queuePeakRecords.Load()
		if value <= peak || reader.queuePeakRecords.CompareAndSwap(peak, value) {
			return
		}
	}
}

func (reader *schedBlamePerfReader) Drain(
	fn func(*schedBlameRecord),
) error {
	// Bound periodic work so a continuous producer cannot starve evaluation.
	queued := len(reader.records)
	for drained := 0; drained < queued; drained++ {
		select {
		case record := <-reader.records:
			fn(record)
			reader.putRecord(record)
		default:
			return reader.getError()
		}
	}
	return reader.getError()
}

func (reader *schedBlamePerfReader) FinalDrain(
	fn func(*schedBlameRecord),
) error {
	for {
		select {
		case record := <-reader.records:
			fn(record)
			reader.putRecord(record)
		case <-reader.done:
			for {
				select {
				case record := <-reader.records:
					fn(record)
					reader.putRecord(record)
				default:
					return reader.getError()
				}
			}
		}
	}
}

func (reader *schedBlamePerfReader) Done() <-chan struct{} {
	return reader.done
}

func (reader *schedBlamePerfReader) LostSamples() uint64 {
	return reader.lostSamples.Load()
}

func (reader *schedBlamePerfReader) deliveredSliceBatchRecords() uint64 {
	return reader.sliceBatchRecords.Load()
}

func (reader *schedBlamePerfReader) deliveredSlices() uint64 {
	return reader.slices.Load()
}

type schedBlamePressureStats struct {
	perfSupported     bool
	perfPeakBytes     uint64
	perfCapacityBytes uint64
	queuePeakRecords  uint64
	queueCapacity     uint64
}

func (reader *schedBlamePerfReader) takePressureStats() schedBlamePressureStats {
	stats := schedBlamePressureStats{
		queuePeakRecords: reader.queuePeakRecords.Swap(0),
		queueCapacity:    uint64(cap(reader.records)),
	}
	// A queue that spans the logging boundary starts the next interval at its
	// current depth rather than zero.
	reader.observeQueueDepth(len(reader.records))
	stats.perfSupported = true
	stats.perfPeakBytes = reader.perfPeakBytes.Swap(0)
	stats.perfCapacityBytes = uint64(reader.reader.PerCPUBufferSize())
	return stats
}

func openSchedBlameEventReader(
	ctx context.Context,
	b ibpf.BPF,
	config *schedBlameRuntimeConfig,
) (*schedBlamePerfReader, error) {
	reader, err := b.RawEventPipeByName(
		ctx,
		"events",
		ibpf.PerfEventReaderOptions{
			PerCPUBufferBytes: config.perfEventPerCPUBufferBytes,
			WatermarkBytes:    config.perfEventWatermarkBytes,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("open perf_event_array: %w", err)
	}
	return newSchedBlamePerfReader(ctx, reader, config.perfEventQueueRecords), nil
}

func readSchedBlameCounter(b ibpf.BPF, mapName string) (uint64, error) {
	mapID := b.MapIDByName(mapName)
	if mapID == 0 {
		return 0, fmt.Errorf("map %q not found", mapName)
	}
	value, err := b.ReadMap(mapID, []byte{0, 0, 0, 0})
	if err != nil {
		return 0, fmt.Errorf("read map %q: %w", mapName, err)
	}
	if len(value) < 8 {
		return 0, fmt.Errorf("read map %q: short value %d", mapName, len(value))
	}
	var total uint64
	for offset := 0; offset+8 <= len(value); offset += 8 {
		total += binary.LittleEndian.Uint64(value[offset : offset+8])
	}
	return total, nil
}

func drainSchedBlameTailBatches(
	b ibpf.BPF,
	fn func(*schedBlameBatch),
) error {
	mapID := b.MapIDByName("slice_batches_percpu")
	if mapID == 0 {
		return errors.New("sched-blame: slice_batches_percpu map not found")
	}
	values, err := b.ReadMap(mapID, []byte{0, 0, 0, 0})
	if err != nil {
		return fmt.Errorf("sched-blame: read slice_batches_percpu: %w", err)
	}
	if len(values)%schedBlameBatchValueSize != 0 {
		return fmt.Errorf(
			"sched-blame: invalid slice_batches_percpu value size: %d",
			len(values),
		)
	}

	valueReader := bytes.NewReader(values)
	raw := make([]byte, schedBlameBatchValueSize)
	for valueReader.Len() != 0 {
		if _, err := io.ReadFull(valueReader, raw); err != nil {
			return fmt.Errorf("sched-blame: read tail batch: %w", err)
		}
		if binary.LittleEndian.Uint32(raw[4:8]) == 0 {
			continue
		}
		batch := new(schedBlameBatch)
		if err := decodeSchedBlameBatch(raw, batch, true); err != nil {
			return err
		}
		fn(batch)
	}
	return nil
}

type schedBlameRunner struct {
	bpf                            ibpf.BPF
	config                         schedBlameRuntimeConfig
	detach                         func() error
	reader                         *schedBlamePerfReader
	state                          *schedBlameState
	uploader                       *schedBlameUploader
	lifecycleSubscription          *pod.ContainerLifecycleSubscription
	targetSyncSignal               chan struct{}
	nextTargetSync                 time.Time
	pollInterval                   time.Duration
	lastEvaluation                 time.Time
	lastStatus                     time.Time
	lastPerfSubmissionFailures     uint64
	lastPerfSubmissionFailedSlices uint64
	lastPerfRecordsLost            uint64
	ratioDebugWriter               *schedBlameExternalRatioDebugWriter
	irmasSampler                   *schedBlameIrmasSampler

	lastDebugStats                      time.Time
	lastDebugPerfSubmissionFailures     uint64
	lastDebugPerfSubmissionFailedSlices uint64
	lastDebugThrottleSubmissionFailures uint64
	lastDebugPerfRecordsLost            uint64
	lastDebugSliceBatchRecords          uint64
	lastDebugSlices                     uint64
	lastDebugUploadDrops                uint64
	attributionInvalid                  bool
}

func (runner *schedBlameRunner) drain() error {
	return runner.reader.Drain(runner.state.handleRecord)
}

func (runner *schedBlameRunner) requestTargetSync() {
	select {
	case runner.targetSyncSignal <- struct{}{}:
	default:
	}
}

func (runner *schedBlameRunner) publishTargets(
	targets *[schedBlameMaxTargets]schedBlameTarget,
) error {
	if err := publishSchedBlameTargets(runner.bpf, runner.state, targets); err != nil {
		runner.attributionInvalid = true
		return err
	}
	return nil
}

func (runner *schedBlameRunner) processTargetSync(
	now time.Time,
	requested bool,
) error {
	if !requested && now.Before(runner.nextTargetSync) {
		return nil
	}

	targets, err := resolveSchedBlameTargets(
		&runner.state.targets,
		&runner.config,
		false,
	)
	if err != nil {
		log.Warnf("sched-blame: synchronize targets: %v", err)
		runner.nextTargetSync = now.Add(schedBlameTargetSyncRetry)
		return nil
	}
	if runner.config.highlightConfigured() &&
		!targets[schedBlameHighlightIndex].active() {
		log.Warnf(
			"sched-blame: highlighted container %q is currently absent",
			runner.config.highlightContainer,
		)
	}
	if schedBlameSameTargetAssignments(&targets, &runner.state.targets) {
		// Keep accumulated data while refreshing names, paths, and identity
		// metadata for unchanged owners.
		runner.state.setTargets(&targets)
	} else if err := runner.publishTargets(&targets); err != nil {
		return err
	}
	runner.nextTargetSync = now.Add(schedBlameTargetSyncInterval)
	return nil
}

func schedBlameCounterDelta(current, previous uint64) uint64 {
	if current < previous {
		return current
	}
	return current - previous
}

func schedBlamePressurePercent(used, capacity uint64) float64 {
	if capacity == 0 {
		return 0
	}
	return float64(used) * 100 / float64(capacity)
}

func schedBlameSliceBatchFillPercent(
	slices uint64,
	batchRecords uint64,
	sliceBatchSize uint32,
) float64 {
	if batchRecords == 0 || sliceBatchSize == 0 {
		return 0
	}
	return float64(slices) * 100 /
		float64(batchRecords*uint64(sliceBatchSize))
}

func (runner *schedBlameRunner) maybeLogTransportStats(now time.Time) {
	if log.GetLevel() < logrus.DebugLevel ||
		now.Sub(runner.lastDebugStats) < schedBlameDebugInterval {
		return
	}

	perfSubmissionFailures, err := readSchedBlameCounter(
		runner.bpf,
		"n_perf_submission_failures",
	)
	if err != nil {
		runner.lastDebugStats = now
		return
	}
	perfSubmissionFailedSlices, err := readSchedBlameCounter(
		runner.bpf,
		"n_perf_submission_failed_slices",
	)
	if err != nil {
		runner.lastDebugStats = now
		return
	}
	throttleSubmissionFailures, err := readSchedBlameCounter(
		runner.bpf,
		"n_throttle_event_submission_failures",
	)
	if err != nil {
		runner.lastDebugStats = now
		return
	}
	perfRecordsLost := runner.reader.LostSamples()
	sliceBatchRecords := runner.reader.deliveredSliceBatchRecords()
	slices := runner.reader.deliveredSlices()
	perfSubmissionFailureDelta := schedBlameCounterDelta(
		perfSubmissionFailures,
		runner.lastDebugPerfSubmissionFailures,
	)
	perfSubmissionFailedSliceDelta := schedBlameCounterDelta(
		perfSubmissionFailedSlices,
		runner.lastDebugPerfSubmissionFailedSlices,
	)
	throttleSubmissionFailureDelta := schedBlameCounterDelta(
		throttleSubmissionFailures,
		runner.lastDebugThrottleSubmissionFailures,
	)
	perfRecordsLostDelta := schedBlameCounterDelta(
		perfRecordsLost,
		runner.lastDebugPerfRecordsLost,
	)
	sliceBatchRecordDelta := schedBlameCounterDelta(
		sliceBatchRecords,
		runner.lastDebugSliceBatchRecords,
	)
	sliceDelta := schedBlameCounterDelta(
		slices,
		runner.lastDebugSlices,
	)
	pressure := runner.reader.takePressureStats()
	uploadQueueLength, uploadQueueCapacity, uploadDrops := runner.uploader.stats()
	uploadDropDelta := schedBlameCounterDelta(
		uploadDrops,
		runner.lastDebugUploadDrops,
	)

	perfRingPeakPercent := "unavailable"
	if pressure.perfSupported {
		perfRingPeakPercent = fmt.Sprintf(
			"%.1f%%",
			schedBlamePressurePercent(
				pressure.perfPeakBytes,
				pressure.perfCapacityBytes,
			),
		)
	}
	log.Debugf(
		"sched-blame: transport interval perf_submission_failures=%d perf_submission_failed_slices=%d throttle_event_submission_failures=%d perf_records_lost=%d slice_batch_records=%d slices=%d slice_batch_fill_pct=%.1f%% perf_ring_peak_pct=%s perf_ring_peak_bytes=%d perf_ring_capacity_bytes=%d go_queue_peak_pct=%.1f%% go_queue_peak_records=%d go_queue_capacity_records=%d upload_queue_records=%d upload_queue_capacity=%d upload_drops=%d",
		perfSubmissionFailureDelta,
		perfSubmissionFailedSliceDelta,
		throttleSubmissionFailureDelta,
		perfRecordsLostDelta,
		sliceBatchRecordDelta,
		sliceDelta,
		schedBlameSliceBatchFillPercent(
			sliceDelta,
			sliceBatchRecordDelta,
			runner.config.sliceBatchSize,
		),
		perfRingPeakPercent,
		pressure.perfPeakBytes,
		pressure.perfCapacityBytes,
		schedBlamePressurePercent(
			pressure.queuePeakRecords,
			pressure.queueCapacity,
		),
		pressure.queuePeakRecords,
		pressure.queueCapacity,
		uploadQueueLength,
		uploadQueueCapacity,
		uploadDropDelta,
	)

	runner.lastDebugStats = now
	runner.lastDebugPerfSubmissionFailures = perfSubmissionFailures
	runner.lastDebugPerfSubmissionFailedSlices = perfSubmissionFailedSlices
	runner.lastDebugThrottleSubmissionFailures = throttleSubmissionFailures
	runner.lastDebugPerfRecordsLost = perfRecordsLost
	runner.lastDebugSliceBatchRecords = sliceBatchRecords
	runner.lastDebugSlices = slices
	runner.lastDebugUploadDrops = uploadDrops
}

func (runner *schedBlameRunner) evaluate(now time.Time) error {
	irmasSamples := runner.sampleHighlightedIrmas()
	_, err := runner.state.evaluateExternalContentionRatiosWithDebug(
		now,
		runner.ratioDebugWriter,
		irmasSamples,
		runner.uploader,
		&runner.config,
	)
	runner.lastEvaluation = now
	return err
}

func (runner *schedBlameRunner) maybeEvaluate(now time.Time) error {
	if now.Sub(runner.lastEvaluation) < schedBlameEvaluationInterval {
		return nil
	}
	return runner.evaluate(now)
}

func (runner *schedBlameRunner) sampleHighlightedIrmas() map[uint64]schedBlameIrmasSample {
	if !runner.config.highlightConfigured() {
		runner.irmasSampler.retain(nil)
		return nil
	}
	activeContainerIDs := make(map[string]struct{})
	samples := make(map[uint64]schedBlameIrmasSample)
	target := runner.state.targets[schedBlameHighlightIndex]
	if !target.active() {
		runner.irmasSampler.retain(activeContainerIDs)
		return samples
	}
	activeContainerIDs[target.containerID] = struct{}{}
	samples[target.cgid] = runner.irmasSampler.sample(
		target.containerID,
		target.cgroupPath,
		target.cgid,
	)
	runner.irmasSampler.retain(activeContainerIDs)
	return samples
}

func (runner *schedBlameRunner) maybeReportStatus(now time.Time) {
	if now.Sub(runner.lastStatus) < schedBlameStatusInterval {
		return
	}

	perfSubmissionFailures, submissionErr := readSchedBlameCounter(
		runner.bpf,
		"n_perf_submission_failures",
	)
	if submissionErr != nil {
		log.Warnf("sched-blame: read perf submission failures: %v",
			submissionErr)
	} else {
		delta := schedBlameCounterDelta(
			perfSubmissionFailures,
			runner.lastPerfSubmissionFailures,
		)
		if delta != 0 {
			log.Warnf(
				"sched-blame: perf submission failures total=%d delta=%d",
				perfSubmissionFailures,
				delta,
			)
		}
		runner.lastPerfSubmissionFailures = perfSubmissionFailures
	}
	perfSubmissionFailedSlices, failedSliceErr := readSchedBlameCounter(
		runner.bpf,
		"n_perf_submission_failed_slices",
	)
	if failedSliceErr != nil {
		log.Warnf("sched-blame: read failed submitted slices: %v",
			failedSliceErr)
	} else {
		delta := schedBlameCounterDelta(
			perfSubmissionFailedSlices,
			runner.lastPerfSubmissionFailedSlices,
		)
		if delta != 0 {
			log.Warnf(
				"sched-blame: failed submitted slices total=%d delta=%d",
				perfSubmissionFailedSlices,
				delta,
			)
		}
		runner.lastPerfSubmissionFailedSlices = perfSubmissionFailedSlices
	}
	perfRecordsLost := runner.reader.LostSamples()
	perfRecordsLostDelta := schedBlameCounterDelta(
		perfRecordsLost,
		runner.lastPerfRecordsLost,
	)
	if perfRecordsLostDelta != 0 {
		log.Warnf("sched-blame: perf records lost total=%d delta=%d",
			perfRecordsLost, perfRecordsLostDelta)
	}
	runner.lastPerfRecordsLost = perfRecordsLost
	sampled, _ := readSchedBlameCounter(runner.bpf, "n_sampled_slices")
	filtered, _ := readSchedBlameCounter(
		runner.bpf,
		"n_slice_probability_drops",
	)
	cssOverflow, _ := readSchedBlameCounter(runner.bpf, "n_css_overflow")
	durationOverflow, _ := readSchedBlameCounter(
		runner.bpf,
		"n_duration_overflow",
	)
	throttleSubmissionFailures, _ := readSchedBlameCounter(
		runner.bpf,
		"n_throttle_event_submission_failures",
	)
	throttleDurationOverflow, _ := readSchedBlameCounter(
		runner.bpf,
		"n_throttle_duration_overflow",
	)
	invalidThrottleDuration, _ := readSchedBlameCounter(
		runner.bpf,
		"n_invalid_throttle_duration",
	)
	invalidRunnableNr, _ := readSchedBlameCounter(
		runner.bpf,
		"n_invalid_runnable_nr",
	)
	invalidTargetDenseID, _ := readSchedBlameCounter(
		runner.bpf,
		"n_invalid_target_dense_id",
	)
	log.Debugf(
		"sched-blame: status targets=%d sampled_slices=%d slice_probability_drops=%d css_overflow=%d duration_overflow=%d throttle_submission_failures=%d throttle_duration_overflow=%d invalid_throttle_duration=%d invalid_throttle_dense_id=%d invalid_runnable_nr=%d invalid_target_dense_id=%d",
		runner.state.activeTargetCount(),
		sampled,
		filtered,
		cssOverflow,
		durationOverflow,
		throttleSubmissionFailures,
		throttleDurationOverflow,
		invalidThrottleDuration,
		runner.state.invalidThrottleDenseIDs,
		invalidRunnableNr,
		invalidTargetDenseID,
	)
	runner.lastStatus = now
}

func (runner *schedBlameRunner) finalDrain() {
	if runner.lifecycleSubscription != nil {
		runner.lifecycleSubscription.Close()
		runner.lifecycleSubscription = nil
	}
	detached := true
	if runner.detach != nil {
		if err := runner.detach(); err != nil {
			detached = false
			log.Warnf("sched-blame: detach before final drain: %v", err)
		}
	}
	readerClosed := false
	closeReader := func() {
		readerClosed = true
		if err := runner.reader.Close(); err != nil {
			log.Warnf("sched-blame: close perf reader: %v", err)
		}
	}
	if detached {
		if err := runner.reader.Flush(); err != nil {
			log.Warnf("sched-blame: flush perf reader: %v", err)
			closeReader()
		}
	} else {
		// Without successful detachment, flushing cannot establish a stable
		// producer boundary. Cancel the pump so teardown cannot wait forever.
		closeReader()
	}
	handleRecord := runner.state.handleRecord
	if runner.attributionInvalid {
		handleRecord = func(*schedBlameRecord) {}
	}
	if err := runner.reader.FinalDrain(handleRecord); err != nil {
		log.Warnf("sched-blame: final drain: %v", err)
	}
	if !readerClosed {
		closeReader()
	}
	if detached && !runner.attributionInvalid {
		if err := drainSchedBlameTailBatches(
			runner.bpf,
			runner.state.handleBatch,
		); err != nil {
			log.Warnf("sched-blame: drain tail batches: %v", err)
		}
	}
	if !runner.attributionInvalid {
		if err := runner.evaluate(time.Now()); err != nil {
			log.Warnf("sched-blame: final evaluation: %v", err)
		}
	}
}

func (runner *schedBlameRunner) run(ctx context.Context) error {
	ticker := time.NewTicker(runner.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return types.ErrExitByCancelCtx
		case <-runner.reader.Done():
			if err := runner.drain(); err != nil {
				return fmt.Errorf("sched-blame: perf reader: %w", err)
			}
			return errors.New("sched-blame: perf reader stopped unexpectedly")
		case <-runner.targetSyncSignal:
			now := time.Now()
			if err := runner.processTargetSync(now, true); err != nil {
				return fmt.Errorf(
					"sched-blame: publish synchronized targets: %w",
					err,
				)
			}
			if err := runner.drain(); err != nil {
				return fmt.Errorf("sched-blame: drain: %w", err)
			}
			if err := runner.maybeEvaluate(now); err != nil {
				return fmt.Errorf("sched-blame: evaluate: %w", err)
			}
			runner.maybeLogTransportStats(now)
			runner.maybeReportStatus(now)
		case now := <-ticker.C:
			if err := runner.processTargetSync(now, false); err != nil {
				return fmt.Errorf(
					"sched-blame: publish synchronized targets: %w",
					err,
				)
			}
			if err := runner.drain(); err != nil {
				return fmt.Errorf("sched-blame: drain: %w", err)
			}
			if err := runner.maybeEvaluate(now); err != nil {
				return fmt.Errorf("sched-blame: evaluate: %w", err)
			}
			runner.maybeLogTransportStats(now)
			runner.maybeReportStatus(now)
		}
	}
}

type schedBlameTracing struct {
	eventWriter schedBlameEventWriter
}

func schedBlameSliceKeepThreshold(sliceDropPercent uint32) (uint32, uint32) {
	if sliceDropPercent == 0 {
		return 0, 1
	}
	if sliceDropPercent >= 100 {
		return 0, 0
	}
	keepPercent := uint64(100 - sliceDropPercent)
	threshold := (uint64(1) << 32) * keepPercent / 100
	return uint32(threshold), 0
}

func (tracer *schedBlameTracing) Start(ctx context.Context) error {
	runtimeConfig := schedBlameRuntimeConfigSnapshot()
	initialTargets := [schedBlameMaxTargets]schedBlameTarget{}
	targets, err := resolveSchedBlameTargets(
		&initialTargets,
		&runtimeConfig,
		true,
	)
	if err != nil {
		return err
	}

	sliceDropPercent := runtimeConfig.sliceDropPercent
	sliceBatchSize := runtimeConfig.sliceBatchSize
	sliceKeepThreshold, keepAllSlices := schedBlameSliceKeepThreshold(sliceDropPercent)
	constants := map[string]any{
		"__SLICE_KEEP_THRESHOLD__": sliceKeepThreshold,
		"__KEEP_ALL_SLICES__":      keepAllSlices,
		"__SLICE_BATCH_SIZE__":     sliceBatchSize,
	}
	b, err := ibpf.LoadBPF("sched_blame.o", constants)
	if err != nil {
		var verifierErr *cebpf.VerifierError
		if errors.As(err, &verifierErr) {
			log.Errorf("sched-blame verifier:\n%-100v", verifierErr)
		}
		return fmt.Errorf("sched-blame: load BPF: %w", err)
	}
	defer b.Close()
	if err := validateSchedBlameBatchMapValueSize(b); err != nil {
		return err
	}

	state := newSchedBlameState(sliceDropPercent)
	if err := publishSchedBlameTargets(b, state, &targets); err != nil {
		return err
	}

	readerCtx, cancelReader := context.WithCancel(context.Background())
	defer cancelReader()
	reader, err := openSchedBlameEventReader(readerCtx, b, &runtimeConfig)
	if err != nil {
		return fmt.Errorf("sched-blame: open event reader: %w", err)
	}
	defer reader.Close()

	uploader := newSchedBlameUploader(tracer.eventWriter)
	defer uploader.close()
	ratioDebugWriter, err := newSchedBlameExternalRatioDebugWriter(
		runtimeConfig.externalRatioDebugFile,
	)
	if err != nil {
		return fmt.Errorf("sched-blame: external ratio debug file: %w", err)
	}
	if ratioDebugWriter != nil {
		defer func() {
			if err := ratioDebugWriter.close(); err != nil {
				log.Warnf(
					"sched-blame: close external ratio debug file: %v",
					err,
				)
			}
		}()
		log.Infof("sched-blame: recording external ratios to %s",
			runtimeConfig.externalRatioDebugFile)
	}
	now := time.Now()
	runner := &schedBlameRunner{
		bpf:              b,
		config:           runtimeConfig,
		reader:           reader,
		state:            state,
		uploader:         uploader,
		targetSyncSignal: make(chan struct{}, 1),
		nextTargetSync:   now.Add(schedBlameTargetSyncInterval),
		ratioDebugWriter: ratioDebugWriter,
		irmasSampler:     newSchedBlameIrmasSampler(),
		pollInterval:     runtimeConfig.pollInterval,
		lastEvaluation:   now,
		lastStatus:       now,
		lastDebugStats:   now,
	}
	runner.lifecycleSubscription = pod.SubscribeContainerLifecycle(
		runner.requestTargetSync,
	)

	if err := b.Attach(); err != nil {
		runner.lifecycleSubscription.Close()
		return fmt.Errorf("sched-blame: attach: %w", err)
	}
	detached := false
	runner.detach = func() error {
		if detached {
			return nil
		}
		if err := b.Detach(); err != nil {
			return err
		}
		detached = true
		return nil
	}
	defer func() {
		if err := runner.detach(); err != nil {
			log.Warnf("sched-blame: detach: %v", err)
		}
	}()
	// Registered after the detach defer so callbacks are quiesced first on
	// every return path. finalDrain performs the same order on cancellation.
	defer runner.lifecycleSubscription.Close()
	defer runner.finalDrain()

	highlightName := "none"
	highlightID := "none"
	if runtimeConfig.highlightConfigured() {
		highlight := state.targets[schedBlameHighlightIndex]
		highlightName = highlight.name
		highlightID = schedBlameShortID(highlight.containerID)
	}
	log.Infof(
		"sched-blame: transport=perf_event_array slice_batch_size=%d queue_records=%d watermark=%d poll=%v slice_drop_percent=%d%% external_anomaly_k=%.2f target_scope=%s%s targets=%d highlight=%s id=%s",
		sliceBatchSize,
		cap(reader.records),
		runtimeConfig.perfEventWatermarkBytes,
		runtimeConfig.pollInterval,
		sliceDropPercent,
		runtimeConfig.externalAnomalyK,
		runtimeConfig.targetContainerScope,
		fmt.Sprintf(" target_qos=%v", runtimeConfig.targetQos),
		state.activeTargetCount(),
		highlightName,
		highlightID,
	)
	return runner.run(ctx)
}

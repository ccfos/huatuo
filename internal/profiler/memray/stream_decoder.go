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

package memray

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sync"

	"github.com/ccfos/huatuo/internal/log"
)

type deltaValue struct {
	Bytes int64
	Count int64
}

// StreamDecoder incrementally consumes an ALL_ALLOCATIONS stream and tracks
// per-interval deltas for both simple allocators (malloc/pymalloc) and ranged
// allocators (mmap/munmap) via interval tracking.
type StreamDecoder struct {
	rd *reader

	mu           sync.Mutex
	deltaAgg     map[locationKey]deltaValue
	skippedRa    uint64
	skippedRf    uint64
	zeroPtrCache uint64
}

func resolveNativeSymbolPID(opt Options, headerPID int32) int32 {
	if opt.NativeSymbolPID > 0 {
		return opt.NativeSymbolPID
	}
	return headerPID
}

// NewStreamDecoder initializes a streaming decoder and reads the header.
func NewStreamDecoder(r io.Reader, opt Options) (*StreamDecoder, Header, error) {
	if opt.Separator == "" {
		opt.Separator = ";"
	}
	if opt.Metric == "" {
		opt.Metric = "bytes"
	}

	rd := &reader{
		r:   r,
		opt: opt,
	}
	if err := rd.readHeader(); err != nil {
		return nil, Header{}, err
	}
	if rd.header.FileFormat != fileFormatAllAllocations {
		return nil, rd.header, fmt.Errorf("unsupported file_format %d", rd.header.FileFormat)
	}
	if opt.StackMode != StackModePython && !rd.header.NativeTraces {
		return nil, rd.header, fmt.Errorf("native/hybrid stacks requested but native_traces is false")
	}

	return &StreamDecoder{
		rd:       rd,
		deltaAgg: make(map[locationKey]deltaValue),
	}, rd.header, nil
}

// NextRecord processes a single record. Only a trailer returns false, nil;
// EOF before the trailer is io.ErrUnexpectedEOF, including between records.
// Call it from one goroutine; FlushDelta may run concurrently.
func (d *StreamDecoder) NextRecord() (bool, error) {
	var tok [1]byte
	if _, err := io.ReadFull(d.rd.r, tok[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return false, fmt.Errorf("memray stream ended before trailer: %w", io.ErrUnexpectedEOF)
		}
		return false, err
	}
	recordType, flags := d.rd.extractTypeAndFlags(tok[0])
	var err error
	switch recordType {
	case recordTypeTrailer:
		return false, nil
	case recordTypeAllocation: // Allocation records cover alloc+free; allocator kind disambiguates.
		err = d.handleAllocationDelta(flags)
	case recordTypeFramePush:
		err = d.rd.handleFramePush(flags)
	case recordTypeFramePop:
		err = d.rd.handleFramePop(flags)
	case recordTypeContextSwitch:
		err = d.rd.handleContextSwitch()
	case recordTypeCodeObject:
		err = d.rd.handleCodeObject()
	case recordTypeThreadRecord:
		// Consumed for stream alignment only. Thread names are not used in
		// retained aggregation/output today.
		_, err = d.rd.readCString()
	case recordTypeMemoryMapStart:
		// Marker only; ignored in profiler retained path.
	case recordTypeSegmentHeader:
		// Mapping metadata (filename/base/segments). Parsed and discarded since
		// symbolization for folded output does not currently use mmap metadata.
		err = d.rd.skipSegmentHeader()
	case recordTypeSegment:
		return false, fmt.Errorf("unexpected SEGMENT without header")
	case recordTypeMemoryRecord:
		// Periodic memory snapshot (rss/timestamp). Not part of retained stack
		// aggregation, so we consume and ignore.
		err = d.rd.skipMemoryRecord()
	case recordTypeNativeTraceIndex:
		err = d.rd.handleNativeFrameIndex()
	case recordTypeObject:
		// Python object lifecycle record. Current retained pipeline aggregates
		// allocation deltas only, so object records are consumed then ignored.
		err = d.rd.skipObjectRecord(flags)
	default:
		return false, fmt.Errorf("unsupported record type %d", recordType)
	}
	if err != nil {
		// Once a token was read, EOF means a missing field, not stream completion.
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return false, fmt.Errorf("decode memray record type %d: %w", recordType, err)
	}
	return true, nil
}

func (d *StreamDecoder) handleAllocationDelta(flags uint8) error {
	ptrIdx := (flags >> 3) & 0x0f
	var addr uint64
	// Pointer decoding mirrors the writer's cache/delta scheme:
	// - ptrIdx is a 4-bit cache index (0-14). 0x0f means "cache miss".
	// - On miss, a signed varint delta follows. lastDataPtr stores addr>>3,
	//   so we add delta then shift left by 3 to recover the full address.
	// - On miss we update recentPtrs[0] (shift others). On hit we only read
	//   recentPtrs[ptrIdx] and do not reorder, matching the encoder cache.
	if ptrIdx == 0x0f {
		delta, err := d.rd.readSignedVarint()
		if err != nil {
			return err
		}
		d.rd.lastDataPtr = uint64(int64(d.rd.lastDataPtr) + delta)
		addr = d.rd.lastDataPtr << 3
		copy(d.rd.recentPtrs[1:], d.rd.recentPtrs[:len(d.rd.recentPtrs)-1])
		d.rd.recentPtrs[0] = addr
	} else {
		addr = d.rd.recentPtrs[ptrIdx]
		if addr == 0 {
			d.zeroPtrCache++
			if d.zeroPtrCache <= 3 {
				log.Warnf("memray pointer cache hit to 0 (idx=%d); stream may be corrupt", ptrIdx)
			}
		}
	}

	allocatorID := flags & 7
	if allocatorID == 0 {
		var b [1]byte
		if _, err := io.ReadFull(d.rd.r, b[:]); err != nil {
			return err
		}
		allocatorID = b[0]
	}

	kind, err := allocatorKind(allocatorID)
	if err != nil {
		return err
	}

	var nativeFrame uint64
	if d.rd.header.NativeTraces && kind != allocatorKindSimpleDealloc {
		delta, err := d.rd.readSignedVarint()
		if err != nil {
			return err
		}
		d.rd.lastNativeFrame = uint64(int64(d.rd.lastNativeFrame) + delta)
		nativeFrame = d.rd.lastNativeFrame
	}

	var size uint64
	if kind == allocatorKindSimpleDealloc {
		size = 0
	} else {
		var err error
		size, err = d.rd.readVarint()
		if err != nil {
			return err
		}
	}

	switch kind {
	case allocatorKindSimpleAlloc:
		return d.addSimpleAlloc(addr, size, nativeFrame)
	case allocatorKindSimpleDealloc:
		d.removeSimpleAlloc(addr)
		return nil
	case allocatorKindRangedAlloc:
		return d.addRangedAlloc(addr, size, nativeFrame)
	case allocatorKindRangedDealloc:
		return d.removeRangedAlloc(addr, size)
	default:
		return fmt.Errorf("unsupported Memray allocator kind %d", kind)
	}
}

func (d *StreamDecoder) currentAllocContext(size, nativeFrame uint64) liveAlloc {
	tid := d.rd.lastThreadID
	stack := d.rd.threadStacks[tid]
	var frameIdx uint64
	if len(stack) > 0 {
		frameIdx = stack[len(stack)-1]
	}
	return liveAlloc{
		Size:        size,
		FrameIdx:    frameIdx,
		NativeFrame: nativeFrame,
		ThreadID:    tid,
	}
}

func (d *StreamDecoder) addSimpleAlloc(addr, size, nativeFrame uint64) error {
	if size > math.MaxInt64 {
		return fmt.Errorf("allocation size %d exceeds MaxInt64", size)
	}
	alloc := d.currentAllocContext(size, nativeFrame)
	d.rd.simpleAllocs[addr] = alloc

	key := locationKey{
		PythonFrameID: alloc.FrameIdx,
		NativeFrameID: alloc.NativeFrame,
		ThreadID:      alloc.ThreadID,
	}
	d.addDelta(key, int64(size), 1)
	return nil
}

func (d *StreamDecoder) addRangedAlloc(addr, size, nativeFrame uint64) error {
	// rangedAllocationEnd rejects sizes above MaxInt64 before overlap state
	// changes, so the signed delta conversion below is safe.
	end, err := rangedAllocationEnd(addr, size)
	if err != nil {
		return err
	}
	if size == 0 {
		d.mu.Lock()
		d.skippedRa++
		d.mu.Unlock()
		return nil
	}

	// MAP_FIXED replaces overlapping mappings without a separate munmap event.
	d.removeRangedOverlaps(addr, end)
	alloc := d.currentAllocContext(size, nativeFrame)
	d.rd.rangeAllocs.intervals = append(d.rd.rangeAllocs.intervals, interval{
		start: addr,
		end:   end,
		alloc: alloc,
	})

	key := locationKey{
		PythonFrameID: alloc.FrameIdx,
		NativeFrameID: alloc.NativeFrame,
		ThreadID:      alloc.ThreadID,
	}
	d.addDelta(key, int64(size), 1)
	return nil
}

func (d *StreamDecoder) removeSimpleAlloc(addr uint64) {
	alloc, ok := d.rd.simpleAllocs[addr]
	if !ok {
		return
	}
	delete(d.rd.simpleAllocs, addr)
	key := locationKey{
		PythonFrameID: alloc.FrameIdx,
		NativeFrameID: alloc.NativeFrame,
		ThreadID:      alloc.ThreadID,
	}
	d.addDelta(key, -int64(alloc.Size), -1)
}

func (d *StreamDecoder) removeRangedAlloc(addr, size uint64) error {
	remEnd, err := rangedAllocationEnd(addr, size)
	if err != nil {
		return err
	}
	if size == 0 {
		d.mu.Lock()
		d.skippedRf++
		d.mu.Unlock()
		return nil
	}
	if !d.removeRangedOverlaps(addr, remEnd) {
		d.mu.Lock()
		d.skippedRf++
		d.mu.Unlock()
	}
	return nil
}

// removeRangedOverlaps subtracts covered bytes from their original allocation
// stacks. Both explicit unmaps and replacement mappings use this accounting.
func (d *StreamDecoder) removeRangedOverlaps(remStart, remEnd uint64) bool {
	// TODO: Run BenchmarkRangedAllocationUpdates in CI before choosing indexed
	// interval storage; scanning every live range can become quadratic.
	intervals := d.rd.rangeAllocs.intervals
	var out []interval
	for i, iv := range intervals {
		// No overlap
		if remEnd <= iv.start || remStart >= iv.end {
			if out != nil {
				out = append(out, iv)
			}
			continue
		}

		// Disjoint mappings need no replacement slice.
		if out == nil {
			out = make([]interval, 0, len(intervals)+1)
			out = append(out, intervals[:i]...)
		}
		overlapStart := remStart
		if iv.start > overlapStart {
			overlapStart = iv.start
		}
		overlapEnd := remEnd
		if iv.end < overlapEnd {
			overlapEnd = iv.end
		}
		removed := overlapEnd - overlapStart

		key := locationKey{
			PythonFrameID: iv.alloc.FrameIdx,
			NativeFrameID: iv.alloc.NativeFrame,
			ThreadID:      iv.alloc.ThreadID,
		}
		if removed > 0 {
			d.addDelta(key, -int64(removed), 0)
		}

		newCount := 0
		if overlapStart > iv.start {
			out = append(out, interval{start: iv.start, end: overlapStart, alloc: iv.alloc})
			newCount++
		}
		if overlapEnd < iv.end {
			out = append(out, interval{start: overlapEnd, end: iv.end, alloc: iv.alloc})
			newCount++
		}

		countDelta := int64(newCount - 1)
		if countDelta != 0 {
			d.addDelta(key, 0, countDelta)
		}
	}
	if out == nil {
		return false
	}
	d.rd.rangeAllocs.intervals = out
	return true
}

func (d *StreamDecoder) addDelta(key locationKey, bytesDelta, countDelta int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	entry := d.deltaAgg[key]
	entry.Bytes += bytesDelta
	entry.Count += countDelta
	d.deltaAgg[key] = entry
}

// FlushDelta returns folded delta lines and clears the delta map.
func (d *StreamDecoder) FlushDelta(headerFrame string) []string {
	d.mu.Lock()
	if len(d.deltaAgg) == 0 {
		d.mu.Unlock()
		return nil
	}

	snapshot := make(map[locationKey]deltaValue, len(d.deltaAgg))
	for key, agg := range d.deltaAgg {
		snapshot[key] = agg
	}
	d.deltaAgg = make(map[locationKey]deltaValue)
	d.mu.Unlock()

	// Preserve thread identity through main-thread trimming. Only rendered
	// stacks may be merged: identical frame IDs can need different trimming.
	valuesByStack := make(map[string]int64, len(snapshot))
	for key, agg := range snapshot {
		var value int64
		if d.rd.opt.Metric == "count" {
			value = agg.Count
		} else {
			value = agg.Bytes
		}
		if value == 0 {
			continue
		}

		d.rd.stackMu.Lock()
		stackStr := d.rd.stackForLocation(key)
		d.rd.stackMu.Unlock()
		if !d.rd.opt.MergeThreads {
			prefix := fmt.Sprintf("tid %d", key.ThreadID)
			if stackStr != "" {
				stackStr = prefix + d.rd.opt.Separator + stackStr
			} else {
				stackStr = prefix
			}
		}
		if headerFrame != "" {
			if stackStr != "" {
				stackStr = headerFrame + d.rd.opt.Separator + stackStr
			} else {
				stackStr = headerFrame
			}
		}
		valuesByStack[stackStr] += value
	}

	lines := make([]string, 0, len(valuesByStack))
	for stack, value := range valuesByStack {
		if value != 0 {
			lines = append(lines, fmt.Sprintf("%s %d", stack, value))
		}
	}
	return lines
}

// SkippedRanged returns counts of skipped ranged alloc/free events.
func (d *StreamDecoder) SkippedRanged() (allocs, frees uint64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.skippedRa, d.skippedRf
}

// BadParents returns the number of malformed parent references seen in the frame tree.
func (d *StreamDecoder) BadParents() uint64 {
	d.rd.stackMu.Lock()
	defer d.rd.stackMu.Unlock()
	return d.rd.frameTree.badParents
}

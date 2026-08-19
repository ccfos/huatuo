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
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/symbol"
)

// TODO: Add recorded allocation/free and malformed/truncated stream fixtures
// so changes to the private Memray stream ABI are detected before release.

func TestReadVarintBounds(t *testing.T) {
	for _, test := range []struct {
		name      string
		encoded   []byte
		want      uint64
		wantError string
	}{
		{"zero", []byte{0}, 0, ""},
		{"one byte", []byte{127}, 127, ""},
		{"two bytes", []byte{128, 1}, 128, ""},
		{"MaxInt64", binary.AppendUvarint(nil, math.MaxInt64), math.MaxInt64, ""},
		{"MaxUint64", binary.AppendUvarint(nil, math.MaxUint64), math.MaxUint64, ""},
		{"overflowing tenth byte", append(bytes.Repeat([]byte{0xff}, 9), 2), 0, "varint overflow"},
		{"continuation in tenth byte", append(bytes.Repeat([]byte{0x80}, 9), 0x80), 0, "varint overflow"},
		{"eleven bytes", append(bytes.Repeat([]byte{0x80}, 10), 1), 0, "varint overflow"},
		{"truncated", []byte{0x80}, 0, "EOF"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rd := &reader{r: bytes.NewReader(test.encoded)}
			got, err := rd.readVarint()
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("readVarint() = %d, %v, want %s", got, err, test.wantError)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("readVarint() = %d, %v, want %d", got, err, test.want)
			}
		})
	}
	for _, want := range []int64{math.MinInt64, -1, 0, 1, math.MaxInt64} {
		rd := &reader{r: bytes.NewReader(binary.AppendVarint(nil, want))}
		if got, err := rd.readSignedVarint(); err != nil || got != want {
			t.Fatalf("readSignedVarint() = %d, %v, want %d", got, err, want)
		}
	}
}

func TestAllocatorKinds(t *testing.T) {
	// IDs match the pinned runtime's hooks::Allocator enum.
	for id, want := range map[uint8]allocatorKindType{
		1: allocatorKindSimpleDealloc, 2: allocatorKindSimpleAlloc,
		3: allocatorKindSimpleAlloc, 4: allocatorKindSimpleAlloc,
		5: allocatorKindSimpleDealloc, 6: allocatorKindSimpleAlloc,
		7: allocatorKindSimpleAlloc, 8: allocatorKindSimpleAlloc,
		9: allocatorKindSimpleAlloc, 10: allocatorKindSimpleAlloc,
		11: allocatorKindSimpleAlloc, 12: allocatorKindSimpleAlloc,
		13: allocatorKindSimpleAlloc, 14: allocatorKindRangedAlloc,
		15: allocatorKindRangedDealloc,
	} {
		if got, err := allocatorKind(id); err != nil || got != want {
			t.Fatalf("allocatorKind(%d) = %d, %v, want %d", id, got, err, want)
		}
	}
	for _, id := range []uint8{0, 16, 255} {
		if _, err := allocatorKind(id); err == nil || !strings.Contains(err.Error(), fmt.Sprint(id)) {
			t.Fatalf("allocatorKind(%d) error = %v, want unsupported ID", id, err)
		}
	}
}

func TestStreamDecoderRejectsInvalidAllocations(t *testing.T) {
	oversized := binary.AppendUvarint(nil, uint64(math.MaxInt64)+1)
	for _, test := range []struct {
		name      string
		allocator uint8
		address   uint64
		size      []byte
		wantError string
	}{
		{"simple size", 6, 0x3000, oversized, "allocation size"},
		{"ranged size", 14, 0x3000, oversized, "allocation size"},
		{"zero address oversized range", 14, 0, oversized, "allocation size"},
		{"overlapping oversized range", 14, 0x2000, oversized, "allocation size"},
		{"ranged free size", 15, 0x3000, oversized, "allocation size"},
		{"range end", 14, math.MaxUint64 - 7, []byte{8}, "allocation range overflows"},
		{"free range end", 15, math.MaxUint64 - 7, []byte{8}, "allocation range overflows"},
		{"size varint", 6, 0x3000, append(bytes.Repeat([]byte{0xff}, 9), 2), "varint overflow"},
		{"truncated size", 6, 0x3000, []byte{0x80}, "unexpected EOF"},
		{"unassigned allocator", 0, 0x3000, nil, "allocator ID 0"},
		{"unknown allocator", 16, 0x3000, nil, "allocator ID 16"},
		{"maximum allocator byte", 255, 0x3000, nil, "allocator ID 255"},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := []byte{recordTypeAllocation}
			if test.allocator > 0 && test.allocator < 8 {
				record[0] |= test.allocator
			} else {
				record = append(record, test.allocator)
			}
			record = append(record, test.size...)
			decoder := &StreamDecoder{
				rd: &reader{
					r: bytes.NewReader(record), lastThreadID: 1,
					recentPtrs: [15]uint64{test.address}, simpleAllocs: make(map[uint64]liveAlloc),
				},
				deltaAgg: make(map[locationKey]deltaValue),
			}
			if err := decoder.addSimpleAlloc(0x1000, 64, 0); err != nil {
				t.Fatal(err)
			}
			if err := decoder.addRangedAlloc(0x2000, 64, 0); err != nil {
				t.Fatal(err)
			}
			if ok, err := decoder.NextRecord(); ok || err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("NextRecord() = %t, %v, want %s", ok, err, test.wantError)
			}
			if len(decoder.rd.simpleAllocs) != 1 || decoder.rd.simpleAllocs[0x1000].Size != 64 ||
				len(decoder.rd.rangeAllocs.intervals) != 1 || decoder.rd.rangeAllocs.intervals[0].end != 0x2040 {
				t.Fatal("invalid record changed live allocations")
			}
			if got := decoder.FlushDelta(""); len(got) != 1 || got[0] != "tid 1 128" {
				t.Fatalf("invalid record changed earlier deltas: %v", got)
			}
		})
	}
}

func TestStreamDecoderAcceptsAllocationBoundaries(t *testing.T) {
	for _, test := range []struct {
		name    string
		ranged  bool
		address uint64
		size    uint64
	}{
		{"zero simple size", false, 0x1000, 0},
		{"maximum simple size", false, 0x1000, math.MaxInt64},
		{"maximum ranged size", true, 0x1000, math.MaxInt64},
		{"maximum range end", true, math.MaxUint64 - 7, 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := []byte{recordTypeAllocation | 6}
			if test.ranged {
				record = []byte{recordTypeAllocation, 14}
			}
			record = binary.AppendUvarint(record, test.size)
			if test.ranged {
				record = append(record, recordTypeAllocation, 15)
				record = binary.AppendUvarint(record, test.size)
			} else {
				record = append(record, recordTypeAllocation|5)
			}
			decoder := &StreamDecoder{
				rd: &reader{
					r: bytes.NewReader(record), lastThreadID: 1,
					recentPtrs: [15]uint64{test.address}, simpleAllocs: make(map[uint64]liveAlloc),
				},
				deltaAgg: make(map[locationKey]deltaValue),
			}
			for _, value := range []int64{int64(test.size), -int64(test.size)} {
				if ok, err := decoder.NextRecord(); !ok || err != nil {
					t.Fatalf("NextRecord() = %t, %v, want complete record", ok, err)
				}
				got := decoder.FlushDelta("")
				if value == 0 {
					if len(got) != 0 {
						t.Fatalf("zero-byte record emitted deltas: %v", got)
					}
				} else if len(got) != 1 || got[0] != fmt.Sprintf("tid 1 %d", value) {
					t.Fatalf("boundary delta = %v, want %d", got, value)
				}
			}
			if len(decoder.rd.simpleAllocs) != 0 || len(decoder.rd.rangeAllocs.intervals) != 0 {
				t.Fatal("boundary free left a live allocation")
			}
		})
	}
}

func TestStreamDecoderReplacesOverlappingMappings(t *testing.T) {
	type mappedRange struct {
		start, end, tid uint64
	}
	for _, test := range []struct {
		name        string
		initial     []mappedRange
		replacement mappedRange
		want        []mappedRange
	}{
		{
			name: "full replacement", initial: []mappedRange{{0x1000, 0x2000, 1}},
			replacement: mappedRange{0x1000, 0x2000, 3}, want: []mappedRange{{0x1000, 0x2000, 3}},
		},
		{
			name: "split original", initial: []mappedRange{{0x1000, 0x4000, 1}},
			replacement: mappedRange{0x2000, 0x3000, 3},
			want:        []mappedRange{{0x1000, 0x2000, 1}, {0x3000, 0x4000, 1}, {0x2000, 0x3000, 3}},
		},
		{
			name: "overlap left edge", initial: []mappedRange{{0x2000, 0x4000, 1}},
			replacement: mappedRange{0x1000, 0x3000, 3},
			want:        []mappedRange{{0x3000, 0x4000, 1}, {0x1000, 0x3000, 3}},
		},
		{
			name: "overlap right edge", initial: []mappedRange{{0x1000, 0x3000, 1}},
			replacement: mappedRange{0x2000, 0x4000, 3},
			want:        []mappedRange{{0x1000, 0x2000, 1}, {0x2000, 0x4000, 3}},
		},
		{
			name: "multiple original stacks", initial: []mappedRange{{0x1000, 0x4000, 1}, {0x5000, 0x8000, 2}},
			replacement: mappedRange{0x2000, 0x6000, 3},
			want:        []mappedRange{{0x1000, 0x2000, 1}, {0x6000, 0x8000, 2}, {0x2000, 0x6000, 3}},
		},
		{
			name: "cover originals and hole", initial: []mappedRange{{0x2000, 0x3000, 1}, {0x5000, 0x6000, 2}},
			replacement: mappedRange{0x1000, 0x7000, 3}, want: []mappedRange{{0x1000, 0x7000, 3}},
		},
		{
			name: "adjacent mappings", initial: []mappedRange{{0x1000, 0x2000, 1}},
			replacement: mappedRange{0x2000, 0x3000, 3},
			want:        []mappedRange{{0x1000, 0x2000, 1}, {0x2000, 0x3000, 3}},
		},
		{
			name: "same allocation stack", initial: []mappedRange{{0x1000, 0x2000, 1}},
			replacement: mappedRange{0x1000, 0x2000, 1}, want: []mappedRange{{0x1000, 0x2000, 1}},
		},
		{
			name: "zero length leaves original", initial: []mappedRange{{0x1000, 0x3000, 1}},
			replacement: mappedRange{0x2000, 0x2000, 3}, want: []mappedRange{{0x1000, 0x3000, 1}},
		},
	} {
		for _, metric := range []string{"bytes", "count"} {
			for _, flushBeforeReplacement := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/flush=%t", test.name, metric, flushBeforeReplacement), func(t *testing.T) {
					rd := &reader{
						opt:          Options{Metric: metric, Separator: ";"},
						codeObjects:  make(map[uint64]codeObject),
						threadStacks: make(map[uint64][]uint64),
					}
					for tid := uint64(1); tid <= 3; tid++ {
						frameID, err := packPythonFrameID(tid, true)
						if err != nil {
							t.Fatal(err)
						}
						rd.codeObjects[tid] = codeObject{Func: fmt.Sprintf("mapping_%d", tid)}
						rd.threadStacks[tid] = []uint64{rd.frameTree.getTraceIndex(0, frameID)}
					}
					decoder := &StreamDecoder{rd: rd, deltaAgg: make(map[locationKey]deltaValue)}
					decodeRange := func(allocator byte, iv mappedRange) {
						t.Helper()
						record := binary.LittleEndian.AppendUint64([]byte{recordTypeContextSwitch}, iv.tid)
						record = append(record, recordTypeAllocation|(15<<3))
						record = binary.AppendVarint(record, int64(iv.start>>3)-int64(rd.lastDataPtr))
						record = append(record, allocator)
						record = binary.AppendUvarint(record, iv.end-iv.start)
						rd.r = bytes.NewReader(record)
						for i := 0; i < 2; i++ {
							if ok, err := decoder.NextRecord(); !ok || err != nil {
								t.Fatalf("NextRecord() = %t, %v", ok, err)
							}
						}
					}
					totalsForRanges := func(ranges []mappedRange) map[uint64]int64 {
						totals := make(map[uint64]int64)
						for _, iv := range ranges {
							value := int64(iv.end - iv.start)
							if metric == "count" {
								value = 1
							}
							totals[iv.tid] += value
						}
						return totals
					}
					assertDeltas := func(wantTotals map[uint64]int64) {
						t.Helper()
						want := make(map[string]bool)
						for tid, value := range wantTotals {
							if value != 0 {
								want[fmt.Sprintf("process;tid %d;mapping_%d %d", tid, tid, value)] = true
							}
						}
						lines := decoder.FlushDelta("process")
						got := make(map[string]bool)
						for _, line := range lines {
							got[line] = true
						}
						if len(lines) != len(want) || !maps.Equal(got, want) {
							t.Fatalf("FlushDelta() = %v, want %v", lines, want)
						}
					}
					for _, iv := range test.initial {
						decodeRange(14, iv)
					}
					initialTotals := totalsForRanges(test.initial)
					if flushBeforeReplacement {
						assertDeltas(initialTotals)
					}
					decodeRange(14, test.replacement)
					retainedTotals := totalsForRanges(test.want)
					replacementDeltas := maps.Clone(retainedTotals)
					if flushBeforeReplacement {
						for tid, value := range initialTotals {
							replacementDeltas[tid] -= value
						}
					}
					assertDeltas(replacementDeltas)
					if len(rd.rangeAllocs.intervals) != len(test.want) {
						t.Fatalf("live intervals = %v, want %v", rd.rangeAllocs.intervals, test.want)
					}
					for i, iv := range rd.rangeAllocs.intervals {
						if got := (mappedRange{iv.start, iv.end, iv.alloc.ThreadID}); got != test.want[i] {
							t.Fatalf("live interval %d = %v, want %v", i, got, test.want[i])
						}
					}
					// Unmapping from another thread must debit each original stack.
					decodeRange(15, mappedRange{0x1000, 0x10000, 99})
					for tid, value := range retainedTotals {
						retainedTotals[tid] = -value
					}
					assertDeltas(retainedTotals)
					if len(rd.rangeAllocs.intervals) != 0 {
						t.Fatalf("munmap left live intervals: %v", rd.rangeAllocs.intervals)
					}
					var wantSkippedAllocs uint64
					if test.replacement.start == test.replacement.end {
						wantSkippedAllocs = 1
					}
					if allocs, frees := decoder.SkippedRanged(); allocs != wantSkippedAllocs || frees != 0 {
						t.Fatalf("SkippedRanged() = %d, %d, want %d, 0", allocs, frees, wantSkippedAllocs)
					}
					rd.r = bytes.NewReader([]byte{recordTypeTrailer})
					if ok, err := decoder.NextRecord(); ok || err != nil {
						t.Fatalf("trailer = %t, %v", ok, err)
					}
				})
			}
		}
	}
}

func BenchmarkRangedAllocationUpdates(b *testing.B) {
	for _, liveRanges := range []int{64, 1024, 16384} {
		for _, operation := range []string{"replace", "map-unmap"} {
			b.Run(fmt.Sprintf("%s/ranges=%d", operation, liveRanges), func(b *testing.B) {
				decoder := &StreamDecoder{
					rd:       &reader{lastThreadID: 1},
					deltaAgg: make(map[locationKey]deltaValue),
				}
				for i := 0; i < liveRanges; i++ {
					start := uint64(0x1000 + i*0x2000)
					decoder.rd.rangeAllocs.intervals = append(decoder.rd.rangeAllocs.intervals, interval{
						start: start, end: start + 4096, alloc: liveAlloc{Size: 4096, ThreadID: 1},
					})
				}
				address := uint64(0x1000 + (liveRanges/2)*0x2000)
				if operation == "map-unmap" {
					address = uint64(0x1000 + liveRanges*0x2000)
				}
				b.ReportAllocs()
				b.SetBytes(4096)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if err := decoder.addRangedAlloc(address, 4096, 0); err != nil {
						b.Fatal(err)
					}
					if operation == "map-unmap" {
						if err := decoder.removeRangedAlloc(address, 4096); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}

func TestStreamDecoderRejectsOversizedLineTable(t *testing.T) {
	record := []byte{recordTypeCodeObject, 1, 'f', 0, 'p', 0, 0}
	record = binary.AppendUvarint(record, uint64(math.MaxInt64)+1)
	decoder := &StreamDecoder{rd: &reader{r: bytes.NewReader(record), codeObjects: make(map[uint64]codeObject)}}
	if ok, err := decoder.NextRecord(); ok || err == nil || !strings.Contains(err.Error(), "line table size") {
		t.Fatalf("NextRecord() = %t, %v, want oversized line table", ok, err)
	}
	if len(decoder.rd.codeObjects) != 0 {
		t.Fatal("oversized line table registered a code object")
	}
}

// Notify the test only after NextRecord reaches the missing record body.
type memrayRecordBodyReader struct {
	io.Reader
	waiting chan struct{}
}

func (r *memrayRecordBodyReader) Read(p []byte) (int, error) {
	if r.waiting != nil {
		close(r.waiting)
		r.waiting = nil
	}
	return r.Reader.Read(p)
}

func TestFlushDeltaWhileRecordBodyIsBlocked(t *testing.T) {
	contextSwitch := binary.LittleEndian.AppendUint64([]byte{recordTypeContextSwitch}, 2)
	segmentHeader := append([]byte{recordTypeSegmentHeader, 0, 1}, make([]byte, 8)...)
	segmentHeader = append(segmentHeader, recordTypeSegment)
	segmentHeader = append(segmentHeader, make([]byte, 8)...)
	segmentHeader = append(segmentHeader, 1)
	for _, test := range []struct {
		name   string
		record []byte
	}{
		{"context switch", contextSwitch},
		{"frame push", []byte{recordTypeFramePush | 1, 1, 0}},
		{"code object", []byte{recordTypeCodeObject, 2, 'f', 0, 0, 0, 0}},
		{"native frame", []byte{recordTypeNativeTraceIndex, 2, 0}},
		{"allocation", []byte{recordTypeAllocation | (15 << 3) | 2, 2, 128, 1}},
		{"thread name", []byte{recordTypeThreadRecord, 't', 0}},
		{"memory record", []byte{recordTypeMemoryRecord, 1, 1}},
		{"segment header", segmentHeader},
		{"object", []byte{recordTypeObject | (15 << 1), 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Decode a real allocation before leaving the next record incomplete.
			rd := &reader{
				r:            bytes.NewReader([]byte{recordTypeAllocation | (15 << 3) | 2, 2, 64}),
				opt:          Options{Separator: ";"},
				lastThreadID: 1,
				codeObjects:  map[uint64]codeObject{1: {Func: "main"}},
				simpleAllocs: make(map[uint64]liveAlloc),
			}
			frameID, err := packPythonFrameID(1, true)
			if err != nil {
				t.Fatal(err)
			}
			rd.threadStacks = map[uint64][]uint64{1: {rd.frameTree.getTraceIndex(0, frameID)}}
			decoder := &StreamDecoder{rd: rd, deltaAgg: make(map[locationKey]deltaValue)}
			if ok, err := decoder.NextRecord(); !ok || err != nil {
				t.Fatalf("decode allocation: ok=%v err=%v", ok, err)
			}

			writer, reader := net.Pipe()
			waiting := make(chan struct{})
			split := len(test.record) - 1
			rd.r = io.MultiReader(bytes.NewReader(test.record[:split]), &memrayRecordBodyReader{
				Reader: reader, waiting: waiting,
			})
			readDone := make(chan struct{})
			readErr := make(chan error, 1)
			go func() {
				defer close(readDone)
				_, err := decoder.NextRecord()
				readErr <- err
			}()
			t.Cleanup(func() {
				_ = writer.Close()
				_ = reader.Close()
				<-readDone
			})
			select {
			case <-waiting:
			case <-time.After(time.Second):
				t.Fatal("decoder did not reach the incomplete record body")
			}

			flushDone := make(chan struct{})
			lines := make(chan []string, 1)
			go func() {
				defer close(flushDone)
				lines <- decoder.FlushDelta("")
			}()
			t.Cleanup(func() {
				_ = reader.Close()
				<-flushDone
			})
			select {
			case got := <-lines:
				if len(got) != 1 || got[0] != "tid 1;main 64" {
					t.Fatalf("FlushDelta() = %v, want [tid 1;main 64]", got)
				}
			case <-time.After(time.Second):
				t.Fatal("FlushDelta blocked on an incomplete record")
			}
			if err := writer.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Write(test.record[split:]); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-readErr:
				if err != nil {
					t.Fatalf("finish record: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("decoder did not finish the record")
			}
		})
	}
}

func TestStreamDecoderRejectsTruncatedRecords(t *testing.T) {
	contextSwitch := binary.LittleEndian.AppendUint64([]byte{recordTypeContextSwitch}, 1)
	segmentHeader := append([]byte{recordTypeSegmentHeader, 'f', 0, 1}, make([]byte, 8)...)
	segmentHeader = append(segmentHeader, recordTypeSegment)
	segmentHeader = append(segmentHeader, make([]byte, 8)...)
	segmentHeader = append(segmentHeader, 128, 1)
	for _, test := range []struct {
		name         string
		record       []byte
		nativeTraces bool
	}{
		{"allocation", []byte{recordTypeAllocation | (15 << 3) | 2, 2, 128, 1}, false},
		{"free", []byte{recordTypeAllocation | (15 << 3) | 1, 0}, false},
		{"native allocation", []byte{recordTypeAllocation | (15 << 3) | 2, 2, 0, 128, 1}, true},
		{"ranged allocation", []byte{recordTypeAllocation | (15 << 3), 2, 14, 128, 1}, false},
		{"ranged free", []byte{recordTypeAllocation | (15 << 3), 2, 15, 128, 1}, false},
		{"context switch", contextSwitch, false},
		{"frame push", []byte{recordTypeFramePush | 1, 1, 128, 1}, false},
		{"code object", []byte{recordTypeCodeObject, 1, 'f', 0, 'p', 0, 2, 2, 1, 2}, false},
		{"native frame", []byte{recordTypeNativeTraceIndex, 128, 1, 0}, true},
		{"thread name", []byte{recordTypeThreadRecord, 't', 0}, false},
		{"memory record", []byte{recordTypeMemoryRecord, 128, 1, 2}, false},
		{"segment header", segmentHeader, false},
		{"object", []byte{recordTypeObject | (15 << 1), 2}, false},
		{"native object", []byte{recordTypeObject | (15 << 1) | 1, 2, 0}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			// Cut at every byte, including field boundaries where io.ReadFull
			// returns EOF instead of ErrUnexpectedEOF. Also accept the full record.
			for end := 1; end <= len(test.record); end++ {
				t.Run(fmt.Sprintf("bytes=%d", end), func(t *testing.T) {
					stream := []byte{recordTypeAllocation | (15 << 3) | 2, 2}
					if test.nativeTraces {
						stream = append(stream, 0)
					}
					stream = append(stream, 64)
					stream = append(stream, test.record[:end]...)
					decoder := &StreamDecoder{
						rd: &reader{
							r:            bytes.NewReader(stream),
							opt:          Options{Separator: ";"},
							header:       Header{NativeTraces: test.nativeTraces},
							threadStacks: make(map[uint64][]uint64),
							codeObjects:  make(map[uint64]codeObject),
							simpleAllocs: make(map[uint64]liveAlloc),
						},
						deltaAgg: make(map[locationKey]deltaValue),
					}
					if ok, err := decoder.NextRecord(); !ok || err != nil {
						t.Fatalf("decode initial allocation: ok=%v err=%v", ok, err)
					}
					ok, err := decoder.NextRecord()
					if end == len(test.record) {
						if !ok || err != nil {
							t.Fatalf("decode complete record: ok=%v err=%v", ok, err)
						}
						if ok, err := decoder.NextRecord(); ok || !errors.Is(err, io.ErrUnexpectedEOF) {
							t.Fatalf("EOF without trailer: ok=%v err=%v, want unexpected EOF", ok, err)
						}
						if test.name == "free" && len(decoder.FlushDelta("")) != 0 {
							t.Fatal("complete free did not cancel the initial allocation")
						}
						return
					}
					if ok || !errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
						t.Fatalf("decode truncated record: ok=%v err=%v, want unexpected EOF", ok, err)
					}
					if got := decoder.FlushDelta(""); len(got) != 1 || got[0] != "tid 0 64" {
						t.Fatalf("truncated record changed earlier allocations: %v", got)
					}
				})
			}
		})
	}
}

func TestStreamDecoderAcceptsTrailer(t *testing.T) {
	decoder := &StreamDecoder{rd: &reader{r: bytes.NewReader([]byte{recordTypeTrailer})}}
	if ok, err := decoder.NextRecord(); ok || err != nil {
		t.Fatalf("decode trailer: ok=%v err=%v", ok, err)
	}
}

func TestStreamDecoderRejectsEOFWithoutRecords(t *testing.T) {
	decoder := &StreamDecoder{rd: &reader{r: bytes.NewReader(nil)}}
	if ok, err := decoder.NextRecord(); ok || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("EOF without trailer: ok=%v err=%v, want unexpected EOF", ok, err)
	}
}

func TestStreamDecoderRejectsIncompleteFreeOnSocketClose(t *testing.T) {
	writer, socketReader := net.Pipe()
	waiting := make(chan struct{})
	decoder := &StreamDecoder{
		rd: &reader{
			r: io.MultiReader(
				// malloc(addr=8, size=64), then a free with its address missing.
				bytes.NewReader([]byte{recordTypeAllocation | (15 << 3) | 2, 2, 64, recordTypeAllocation | (15 << 3) | 1}),
				&memrayRecordBodyReader{Reader: socketReader, waiting: waiting},
			),
			opt:          Options{Separator: ";"},
			simpleAllocs: make(map[uint64]liveAlloc),
		},
		deltaAgg: make(map[locationKey]deltaValue),
	}
	defer writer.Close()
	defer socketReader.Close()
	if ok, err := decoder.NextRecord(); !ok || err != nil {
		t.Fatalf("decode allocation: ok=%v err=%v", ok, err)
	}
	readDone := make(chan struct{})
	readErr := make(chan error, 1)
	go func() {
		defer close(readDone)
		_, err := decoder.NextRecord()
		readErr <- err
	}()
	t.Cleanup(func() {
		_ = socketReader.Close()
		<-readDone
	})
	select {
	case <-waiting:
	case <-time.After(time.Second):
		t.Fatal("decoder did not reach the missing free address")
	}
	// Match the shutdown watchdog closing a stalled stream. The 64-byte live
	// balance is now incomplete, so reader completion must fail, not return nil.
	_ = socketReader.Close()
	select {
	case err := <-readErr:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("incomplete free error = %v, want closed pipe", err)
		}
	case <-time.After(time.Second):
		t.Fatal("decoder remained blocked after socket close")
	}
	if got := decoder.FlushDelta(""); len(got) != 1 || got[0] != "tid 0 64" {
		t.Fatalf("incomplete free changed earlier allocations: %v", got)
	}
}

func TestResolveNativeSymbolPID(t *testing.T) {
	const (
		headerPID = int32(1)
		hostPID   = int32(12345)
	)

	if got := resolveNativeSymbolPID(Options{}, headerPID); got != headerPID {
		t.Fatalf("default symbol PID = %d, want %d", got, headerPID)
	}
	if got := resolveNativeSymbolPID(
		Options{NativeSymbolPID: hostPID},
		headerPID,
	); got != hostPID {
		t.Fatalf("overridden symbol PID = %d, want %d", got, hostPID)
	}
}

func TestNewStreamDecoderRejectsOversizedHeader(t *testing.T) {
	writer, reader := net.Pipe()
	defer writer.Close()
	defer reader.Close()

	header := make([]byte, 0, maxCStringBytes+50)
	header = append(header, []byte("memray\x00")...)
	header = binary.LittleEndian.AppendUint32(header, 12)
	header = binary.LittleEndian.AppendUint32(header, 0)
	header = append(header, 0, fileFormatAllAllocations)
	header = append(header, make([]byte, 32)...)
	header = append(header, bytes.Repeat([]byte{'x'}, maxCStringBytes+1)...)

	writeErr := make(chan error, 1)
	go func() {
		_, err := writer.Write(header)
		writeErr <- err
	}()

	_, _, err := NewStreamDecoder(reader, Options{})
	if err == nil || !strings.Contains(err.Error(), "C string exceeds") {
		t.Fatalf("NewStreamDecoder() error = %v, want oversized C string error", err)
	}

	select {
	case err := <-writeErr:
		if err != nil {
			t.Fatalf("write header: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("header writer did not finish")
	}
}

func TestFlushDeltaTrimsBeforeMergingThreads(t *testing.T) {
	for _, mode := range []StackMode{StackModePython, StackModeHybrid} {
		for _, merge := range []bool{false, true} {
			for _, metric := range []string{"bytes", "count"} {
				t.Run(fmt.Sprintf("mode=%d/merge=%t/metric=%s", mode, merge, metric), func(t *testing.T) {
					rd := &reader{
						opt:          Options{StackMode: mode, MergeThreads: merge, Metric: metric, Separator: ";"},
						header:       Header{MainTid: 11, SkipFrames: 1},
						codeObjects:  map[uint64]codeObject{1: {Func: "attach"}, 2: {Func: "work"}},
						threadStacks: make(map[uint64][]uint64),
						simpleAllocs: make(map[uint64]liveAlloc),
						nativeFrames: []nativeFrame{
							{IP: 1},
							{IP: 2, Next: 1},
							{IP: 2, Next: 2},
							{IP: 3, Next: 3},
							{IP: 2},
							{IP: 3, Next: 5},
						},
						nativeSymbolize: &nativeSymbolizer{
							usym:  &symbol.UsymResolver{},
							cache: map[uint64]string{1: "native_root", 2: "_PyEval_EvalFrameDefault", 3: "malloc"},
						},
					}
					attachID, err := packPythonFrameID(1, true)
					if err != nil {
						t.Fatal(err)
					}
					workID, err := packPythonFrameID(2, true)
					if err != nil {
						t.Fatal(err)
					}
					attachNode := rd.frameTree.getTraceIndex(0, attachID)
					fullStack := rd.frameTree.getTraceIndex(attachNode, workID)
					shortStack := rd.frameTree.getTraceIndex(0, workID)
					tids := []uint64{11, 22, 33, 44}
					stacks := []uint64{fullStack, fullStack, shortStack, shortStack}
					nativeIDs := []uint64{4, 4, 6, 6}
					sizes := []uint64{64, 32, 16, 8}
					decoder := &StreamDecoder{rd: rd, deltaAgg: make(map[locationKey]deltaValue)}
					for i, tid := range tids {
						rd.lastThreadID = tid
						rd.threadStacks[tid] = []uint64{stacks[i]}
						if err := decoder.addSimpleAlloc(0x1000+tid, sizes[i], nativeIDs[i]); err != nil {
							t.Fatal(err)
						}
					}
					rd.lastThreadID = tids[0]
					if err := decoder.addRangedAlloc(0x5000, 128, nativeIDs[0]); err != nil {
						t.Fatal(err)
					}

					assertDelta := func(phase string, byteValues, counts []int64) {
						t.Helper()
						values := byteValues
						if metric == "count" {
							values = counts
						}
						want := make(map[string]int64)
						for i, tid := range tids {
							stack := "work"
							if i == 1 {
								stack = "attach;work"
							}
							if mode == StackModeHybrid {
								stack += ";malloc"
								if i == 1 {
									stack = "native_root;" + stack
								}
							}
							if !merge {
								stack = fmt.Sprintf("tid %d;%s", tid, stack)
							}
							want["process;"+stack] += values[i]
						}
						for stack, value := range want {
							if value == 0 {
								delete(want, stack)
							}
						}
						got := make(map[string]int64)
						for _, line := range decoder.FlushDelta("process") {
							separator := strings.LastIndexByte(line, ' ')
							if separator < 0 {
								t.Fatalf("%s: invalid delta %q", phase, line)
							}
							stack := line[:separator]
							value, err := strconv.ParseInt(line[separator+1:], 10, 64)
							if err != nil {
								t.Fatalf("%s: invalid delta %q", phase, line)
							}
							if _, exists := got[stack]; exists {
								t.Fatalf("%s: duplicate rendered stack %q", phase, stack)
							}
							got[stack] = value
						}
						if !maps.Equal(got, want) {
							t.Fatalf("%s: deltas = %v, want %v", phase, got, want)
						}
					}
					assertDelta("allocations", []int64{192, 32, 16, 8}, []int64{2, 1, 1, 1})

					// Frees must retain the allocating thread, not the freeing thread.
					rd.lastThreadID = 99
					for _, tid := range tids {
						decoder.removeSimpleAlloc(0x1000 + tid)
					}
					if err := decoder.removeRangedAlloc(0x5020, 64); err != nil {
						t.Fatal(err)
					}
					assertDelta("frees and split", []int64{-128, -32, -16, -8}, []int64{0, -1, -1, -1})
					if err := decoder.removeRangedAlloc(0x5000, 128); err != nil {
						t.Fatal(err)
					}
					assertDelta("remaining ranges", []int64{-64, 0, 0, 0}, []int64{-2, 0, 0, 0})
				})
			}
		}
	}
}

func TestStreamDecoderNativeModeKeepsNativeStacks(t *testing.T) {
	for _, test := range []struct {
		name          string
		nativeFrameID uint64
		wantStack     string
	}{
		{"missing native frames", 0, "[unknown native stack]"},
		{"unavailable native frames", 3, "[unknown native stack]"},
		{"unsymbolized native frames", 2, "0x10;0x20"},
	} {
		for _, mergeThreads := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/merge=%t", test.name, mergeThreads), func(t *testing.T) {
				rd := &reader{
					opt:          Options{StackMode: StackModeNative, MergeThreads: mergeThreads, Separator: ";"},
					codeObjects:  map[uint64]codeObject{1: {Func: "python_caller"}},
					lastThreadID: 7,
					nativeFrames: []nativeFrame{{IP: 0x10}, {IP: 0x20, Next: 1}},
					simpleAllocs: make(map[uint64]liveAlloc),
				}
				frameID, err := packPythonFrameID(1, true)
				if err != nil {
					t.Fatal(err)
				}
				frameIndex := rd.frameTree.getTraceIndex(0, frameID)
				rd.threadStacks = map[uint64][]uint64{7: {frameIndex}}
				decoder := &StreamDecoder{rd: rd, deltaAgg: make(map[locationKey]deltaValue)}
				if err := decoder.addSimpleAlloc(0x1000, 64, test.nativeFrameID); err != nil {
					t.Fatal(err)
				}
				stack := test.wantStack
				if !mergeThreads {
					stack = "tid 7;" + stack
				}
				for _, value := range []int64{64, -64} {
					if value < 0 {
						rd.lastThreadID = 99
						decoder.removeSimpleAlloc(0x1000)
					}
					want := fmt.Sprintf("process;%s %d", stack, value)
					if got := decoder.FlushDelta("process"); len(got) != 1 || got[0] != want {
						t.Fatalf("FlushDelta() = %v, want %q", got, want)
					}
				}
			})
		}
	}
}

func TestStreamDecoderHybridKeepsNativeOnlyRetainedBytes(t *testing.T) {
	for _, mergeThreads := range []bool{false, true} {
		t.Run(fmt.Sprintf("merge=%t", mergeThreads), func(t *testing.T) {
			rd := &reader{
				opt:          Options{StackMode: StackModeHybrid, MergeThreads: mergeThreads, Separator: ";"},
				lastThreadID: 7,
				nativeFrames: []nativeFrame{{IP: 0x10}, {IP: 0x20, Next: 1}},
				simpleAllocs: make(map[uint64]liveAlloc),
			}
			decoder := &StreamDecoder{rd: rd, deltaAgg: make(map[locationKey]deltaValue)}
			if err := decoder.addSimpleAlloc(0x1000, 64, 2); err != nil {
				t.Fatal(err)
			}
			if err := decoder.addSimpleAlloc(0x2000, 32, 2); err != nil {
				t.Fatal(err)
			}
			rd.lastThreadID = 99
			decoder.removeSimpleAlloc(0x1000)
			stack := "0x10;0x20"
			if !mergeThreads {
				stack = "tid 7;" + stack
			}
			want := fmt.Sprintf("process;%s 32", stack)
			if got := decoder.FlushDelta("process"); len(got) != 1 || got[0] != want {
				t.Fatalf("retained native-only bytes = %v, want %q", got, want)
			}
			decoder.removeSimpleAlloc(0x2000)
			want = fmt.Sprintf("process;%s -32", stack)
			if got := decoder.FlushDelta("process"); len(got) != 1 || got[0] != want {
				t.Fatalf("remaining native-only free = %v, want %q", got, want)
			}
			if len(rd.simpleAllocs) != 0 {
				t.Fatal("freed native-only allocations remain live")
			}
		})
	}
}

func TestStreamDecoderHandlesOversizedSkipFrames(t *testing.T) {
	headerBytes := make([]byte, 0, 128)
	headerBytes = append(headerBytes, []byte("memray\x00")...)
	headerBytes = binary.LittleEndian.AppendUint32(headerBytes, 12)
	headerBytes = binary.LittleEndian.AppendUint32(headerBytes, 0)
	headerBytes = append(headerBytes, 0, fileFormatAllAllocations)
	headerBytes = append(headerBytes, make([]byte, 32)...)
	headerBytes = append(headerBytes, 0)
	headerBytes = binary.LittleEndian.AppendUint32(headerBytes, 1)
	headerBytes = binary.LittleEndian.AppendUint64(headerBytes, 1)
	headerBytes = binary.LittleEndian.AppendUint64(headerBytes, math.MaxUint64)
	headerBytes = append(headerBytes, 0, 0, 0)

	decoder, header, err := NewStreamDecoder(bytes.NewReader(headerBytes), Options{})
	if err != nil {
		t.Fatalf("NewStreamDecoder() error = %v", err)
	}
	frameID, err := packPythonFrameID(1, true)
	if err != nil {
		t.Fatalf("packPythonFrameID() error = %v", err)
	}
	decoder.rd.codeObjects[1] = codeObject{Func: "main"}
	frameIndex := decoder.rd.frameTree.getTraceIndex(0, frameID)

	got := decoder.rd.stackForLocation(locationKey{
		PythonFrameID: frameIndex,
		ThreadID:      header.MainTid,
	})
	if got != "" {
		t.Fatalf("stackForLocation() = %q, want empty stack", got)
	}
}

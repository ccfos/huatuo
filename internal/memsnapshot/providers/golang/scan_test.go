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

package golang

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestScanHeapProfileAcrossBatches(t *testing.T) {
	memory, info, _ := bucketFixture(t, mbucketBatchSize+1)
	result, err := (&processReader{memory: *memory, runtime: info}).scanHeapProfile(t.Context(), 1)
	if err != nil || result.status != memsnapshot.SnapshotStatusComplete {
		t.Fatalf("scan = %+v, %v", result, err)
	}
	entries := result.allocations
	if !result.hasOmittedAllocations || len(entries) != 1 ||
		entries[0].inuseObjects != 33 || entries[0].inuseBytes != 33*128 ||
		binary.LittleEndian.Uint64([]byte(entries[0].key)) != 1 {
		t.Fatalf("global aggregation across batches = %+v", entries)
	}
}

func TestScanHeapProfileTopKTruncation(t *testing.T) {
	for _, limit := range []int{1, 2, 3, 4} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			memory, info, raw := allocationFixture(t, 3, 1, 3)
			stride := bucketHeaderBytes + programCounterBytes + heapProfileRecordBytes
			for i, size := range []uint64{90, 100, 90} {
				record := 8 + i*stride + bucketHeaderBytes + programCounterBytes
				binary.LittleEndian.PutUint64(raw[record+16:], size)
			}
			result, err := (&processReader{memory: *memory, runtime: info}).scanHeapProfile(t.Context(), limit)
			if err != nil {
				t.Fatal(err)
			}
			if result.status != memsnapshot.SnapshotStatusComplete || len(result.allocations) != min(limit, 3) || result.hasOmittedAllocations != (limit < 3) {
				t.Fatalf("scan limit=%d: %+v", limit, result)
			}
			want := []uint64{100, 90, 90}
			for i, entry := range result.allocations {
				if entry.inuseBytes != want[i] {
					t.Fatalf("scan limit=%d: %+v; want weights %v", limit, result.allocations, want[:min(limit, 3)])
				}
			}
		})
	}
}

func TestScanHeapProfileHeaderReadFailure(t *testing.T) {
	for _, count := range []int{0, 1, mbucketBatchSize, mbucketBatchSize + 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			memory, info, raw := bucketFixture(t, count)
			if count == 0 {
				info.mbucketsHead = 1
			} else {
				stride := bucketHeaderBytes + programCounterBytes + heapProfileRecordBytes
				binary.LittleEndian.PutUint64(raw[8+(count-1)*stride+8:], 1)
			}
			result, err := (&processReader{memory: *memory, runtime: info}).scanHeapProfile(t.Context(), 2)
			if result != nil || !errors.Is(err, unix.EFAULT) || !strings.Contains(err.Error(), "read mbucket header") {
				t.Fatalf("header read failure must discard all samples: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestScanHeapProfileSampleRate(t *testing.T) {
	for _, test := range []struct {
		name   string
		rate   int64
		status memsnapshot.Status
		reason string
	}{
		{name: "unknown", rate: -1, status: memsnapshot.SnapshotStatusUnavailable, reason: "runtime.MemProfileRate is unavailable"},
		{name: "disabled", rate: 0, status: memsnapshot.SnapshotStatusUnavailable, reason: "Go heap profiling is disabled by MemProfileRate=0"},
		{name: "enabled", rate: 1, status: memsnapshot.SnapshotStatusComplete},
	} {
		t.Run(test.name, func(t *testing.T) {
			memory, info, _ := bucketFixture(t, 1)
			info.memProfileRate = test.rate
			scan, err := (&processReader{memory: *memory, runtime: info}).scanHeapProfile(t.Context(), 2)
			if err != nil || scan == nil || scan.status != test.status || scan.reason != test.reason {
				t.Fatalf("scan=%+v err=%v", scan, err)
			}
			if test.rate <= 0 {
				if len(scan.allocations) != 0 || scan.hasOmittedAllocations {
					t.Fatalf("unavailable scan=%+v", scan)
				}
			} else if scan.reason != "" || len(scan.allocations) != 1 {
				t.Fatalf("enabled sample rate scan=%+v", scan)
			}

			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if scan, err := (&processReader{memory: *memory, runtime: info}).scanHeapProfile(ctx, 2); scan != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation must take precedence: scan=%+v err=%v", scan, err)
			}
		})
	}
}

func TestScanHeapProfileSampleReadFailure(t *testing.T) {
	pageSize := os.Getpagesize()
	for _, test := range []struct {
		name  string
		depth int
	}{
		{name: "records", depth: 1},
		{name: "stacks", depth: pageSize / programCounterBytes},
	} {
		for _, prefix := range []int{0, mbucketBatchSize} {
			t.Run(fmt.Sprintf("%s/prefix=%d", test.name, prefix), func(t *testing.T) {
				if test.depth > 1024 {
					t.Skip("a supported stack cannot span an inaccessible page on this system")
				}
				count := prefix + mbucketBatchSize + 1
				memory, info, raw := allocationFixture(t, count, 1, count)
				pages, err := unix.Mmap(-1, 0, 3*pageSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANON)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if err := unix.Munmap(pages); err != nil {
						t.Error(err)
					}
				})
				// Failing after a complete batch must also discard previously aggregated data.
				stride := bucketHeaderBytes + programCounterBytes + heapProfileRecordBytes
				next := raw[8+prefix*stride+8:]
				header := pages[pageSize-bucketHeaderBytes : pageSize]
				binary.LittleEndian.PutUint64(header[8:], binary.LittleEndian.Uint64(next))
				binary.LittleEndian.PutUint64(header[16:], 1)
				binary.LittleEndian.PutUint64(header[40:], uint64(test.depth))
				record := pages[pageSize+test.depth*programCounterBytes:]
				binary.LittleEndian.PutUint64(record, 1)
				binary.LittleEndian.PutUint64(record[16:], 128)
				binary.LittleEndian.PutUint64(next, uint64(uintptr(unsafe.Pointer(&header[0]))))
				if err := unix.Mprotect(pages[pageSize:2*pageSize], unix.PROT_NONE); err != nil {
					t.Fatal(err)
				}

				result, err := (&processReader{memory: *memory, runtime: info}).scanHeapProfile(t.Context(), count)
				wantError := "read mbucket " + test.name
				if result != nil || err == nil || !strings.Contains(err.Error(), wantError) {
					t.Fatalf("sample read failure must discard all samples: result=%+v err=%v, want %q", result, err, wantError)
				}
			})
		}
	}
}

func TestScanHeapProfileCycles(t *testing.T) {
	for _, count := range []int{1, 2, mbucketBatchSize + 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			memory, info, raw := bucketFixture(t, count)
			stride := bucketHeaderBytes + 8 + heapProfileRecordBytes
			binary.LittleEndian.PutUint64(raw[8+(count-1)*stride+8:], info.mbucketsHead)
			result, err := (&processReader{memory: *memory, runtime: info}).scanHeapProfile(t.Context(), 2)
			if err != nil {
				t.Fatal(err)
			}
			var objects uint64
			for _, entry := range result.allocations {
				objects += entry.inuseObjects
			}
			if objects != uint64(count) || !strings.Contains(result.reason, "cycle") || result.status != memsnapshot.SnapshotStatusPartial {
				t.Fatalf("objects=%d reason=%q", objects, result.reason)
			}
		})
	}
}

func TestScanHeapProfileDeepStacks(t *testing.T) {
	for _, depth := range []int{65, 128, 1024, 1024 + 1} {
		t.Run(fmt.Sprint(depth), func(t *testing.T) {
			memory, info, _ := allocationFixture(t, 2, depth, 2)
			result, err := (&processReader{memory: *memory, runtime: info}).scanHeapProfile(t.Context(), 2)
			if err != nil {
				t.Fatal(err)
			}
			entries := result.allocations
			if depth > 1024 {
				if len(entries) != 0 || !strings.Contains(result.reason, "stack depth") || result.status != memsnapshot.SnapshotStatusPartial {
					t.Fatalf("oversized stack: %+v", result)
				}
				return
			}
			if len(entries) != 2 || result.status != memsnapshot.SnapshotStatusComplete {
				t.Fatalf("distinct stacks lost: %+v", result)
			}
			for _, entry := range entries {
				if len(entry.key) != depth*8 || entry.inuseObjects != 1 {
					t.Fatalf("entry=%+v", entry)
				}
			}
			output, err := buildEntries(t.Context(), entries, binary.LittleEndian, nil)
			if err != nil {
				t.Fatal(err)
			}
			snapshot := &memsnapshot.Snapshot{Status: memsnapshot.SnapshotStatusComplete, Entries: output}
			if err := memsnapshot.LimitOutput(snapshot, 2); err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Entries) != 2 || !snapshot.OutputTruncated || snapshot.Status != memsnapshot.SnapshotStatusComplete || len(snapshot.Entries[0].Stack) != 64 {
				t.Fatalf("output=%+v", snapshot)
			}
		})
	}
}

func TestScanHeapProfileZeroDepthStack(t *testing.T) {
	memory, info, _ := allocationFixture(t, 1, 0, 1)
	result, err := (&processReader{memory: *memory, runtime: info}).scanHeapProfile(t.Context(), 1)
	if err != nil || result == nil || result.status != memsnapshot.SnapshotStatusComplete || result.reason != "" ||
		len(result.allocations) != 0 || result.hasOmittedAllocations {
		t.Fatalf("zero-depth stack must not mark the scan partial: result=%+v err=%v", result, err)
	}
}

func BenchmarkCollectAllocations(b *testing.B) {
	for _, test := range []struct {
		name                 string
		count, depth, unique int
	}{
		{"high_cardinality", 8192, 8, 8192},
		{"deep_stacks", 256, 1024, 256},
		{"key_budget", 4097, 1024, 4097},
	} {
		b.Run(test.name, func(b *testing.B) {
			memory, info, _ := allocationFixture(b, test.count, test.depth, test.unique)
			reader := &processReader{memory: *memory, runtime: info}
			b.ReportAllocs()
			for b.Loop() {
				result, err := reader.scanHeapProfile(b.Context(), 100)
				if err != nil {
					b.Fatal(err)
				}

				if (result.status != memsnapshot.SnapshotStatusComplete) != (test.name == "key_budget") {
					b.Fatalf("reason=%q", result.reason)
				}
				top := result.allocations
				if len(top) != 100 || top[0].inuseObjects != 1 || !result.hasOmittedAllocations {
					b.Fatal("invalid top-K")
				}
			}
		})
	}
}

func TestScanHeapProfileInvalidLayout(t *testing.T) {
	for _, test := range []struct {
		name              string
		typ, depth, limit uint64
		reason            string
	}{
		{"wrong type", 2, 1, 1024, "profile type"},
		{"old runtime depth", 1, 33, 32, "stack depth"},
	} {
		t.Run(test.name, func(t *testing.T) {
			memory, info, raw := bucketFixture(t, 2)
			info.layout.maxStackDepth = test.limit
			stride := bucketHeaderBytes + programCounterBytes + heapProfileRecordBytes
			second := 8 + stride
			binary.LittleEndian.PutUint64(raw[second+16:], test.typ)
			binary.LittleEndian.PutUint64(raw[second+40:], test.depth)
			result, err := (&processReader{memory: *memory, runtime: info}).scanHeapProfile(t.Context(), 2)
			if err != nil || !strings.Contains(result.reason, test.reason) || result.status != memsnapshot.SnapshotStatusPartial || len(result.allocations) != 1 {
				t.Fatalf("invalid layout scan: %+v %v", result, err)
			}
		})
	}
}

func TestScanHeapProfileUnknownSampleRate(t *testing.T) {
	memory, info, _ := bucketFixture(t, 2)
	reader := &processReader{memory: *memory, runtime: info}
	info.memProfileRate = -1
	for _, head := range []uint64{0, info.mbucketsHead, 1} {
		t.Run(fmt.Sprint(head), func(t *testing.T) {
			info.mbucketsHead = head
			scan, err := reader.scanHeapProfile(t.Context(), 2)
			if err != nil || scan.status != memsnapshot.SnapshotStatusUnavailable ||
				scan.reason != "runtime.MemProfileRate is unavailable" || len(scan.allocations) != 0 || scan.hasOmittedAllocations {
				t.Fatalf("unknown rate must stop before reading buckets: scan=%+v err=%v", scan, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if scan, err := reader.scanHeapProfile(ctx, 2); scan != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation must take precedence over unknown rate: scan=%+v err=%v", scan, err)
	}
}

func TestScanHeapProfileRanksAllBuckets(t *testing.T) {
	const count = mbucketBatchSize + 1
	memory, info, raw := allocationFixture(t, count, 1, count)
	info.memProfileRate = 1
	stride := bucketHeaderBytes + programCounterBytes + heapProfileRecordBytes
	last := 8 + (count-1)*stride
	record := last + bucketHeaderBytes + programCounterBytes
	binary.LittleEndian.PutUint64(raw[record+16:], 4096)
	reader := &processReader{memory: *memory, runtime: info}
	for _, partial := range []bool{false, true} {
		wantStatus := memsnapshot.SnapshotStatusComplete
		if partial {
			binary.LittleEndian.PutUint64(raw[last+8:], info.mbucketsHead)
			wantStatus = memsnapshot.SnapshotStatusPartial
		}
		result, err := reader.scanHeapProfile(t.Context(), 1)
		if err != nil || result.status != wantStatus || !result.hasOmittedAllocations || len(result.allocations) != 1 {
			t.Fatalf("ranked scan=%+v err=%v", result, err)
		}
		best := result.allocations[0]
		if best.inuseBytes != 4096 || best.inuseObjects != 1 || binary.LittleEndian.Uint64([]byte(best.key)) != count {
			t.Fatalf("last bucket was not ranked first: %+v", best)
		}
	}
}

func TestScanHeapProfileEmpty(t *testing.T) {
	for _, rate := range []int64{-1, 0, 1} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			memory, info, _ := bucketFixture(t, 0)
			info.memProfileRate = rate
			result, err := (&processReader{memory: *memory, runtime: info}).scanHeapProfile(t.Context(), 1)
			wantReason := "Go heap profile contains no buckets"
			if rate < 0 {
				wantReason = "runtime.MemProfileRate is unavailable"
			} else if rate == 0 {
				wantReason = "Go heap profiling is disabled by MemProfileRate=0"
			}
			if err != nil || result == nil || result.status != memsnapshot.SnapshotStatusUnavailable ||
				len(result.allocations) != 0 || result.hasOmittedAllocations {
				t.Fatalf("empty scan=%+v err=%v", result, err)
			}
			if result.reason != wantReason {
				t.Fatalf("empty scan reason=%q, want %q", result.reason, wantReason)
			}
		})
	}
}

func TestScanHeapProfilePublishedStatistics(t *testing.T) {
	const count = mbucketBatchSize + 1
	for _, test := range []struct {
		name        string
		activeIndex int
		future      bool
		allFreed    bool
		cycle       bool
		status      memsnapshot.Status
		reason      string
		entries     int
	}{
		{name: "unpublished", activeIndex: -1, status: memsnapshot.SnapshotStatusUnavailable, reason: "Go heap profile has no published statistics"},
		{name: "future only", activeIndex: -1, future: true, status: memsnapshot.SnapshotStatusUnavailable, reason: "Go heap profile has no published statistics"},
		{name: "active first", activeIndex: 0, future: true, status: memsnapshot.SnapshotStatusComplete, entries: 1},
		{name: "active last", activeIndex: count - 1, future: true, status: memsnapshot.SnapshotStatusComplete, entries: 1},
		{name: "all freed first", activeIndex: 0, future: true, allFreed: true, status: memsnapshot.SnapshotStatusComplete},
		{name: "all freed last", activeIndex: count - 1, future: true, allFreed: true, status: memsnapshot.SnapshotStatusComplete},
		{name: "partial without published data", activeIndex: -1, future: true, cycle: true, status: memsnapshot.SnapshotStatusPartial, reason: "mbucket chain contains a cycle"},
	} {
		t.Run(test.name, func(t *testing.T) {
			memory, info, raw := allocationFixture(t, count, 1, count)
			stride := bucketHeaderBytes + programCounterBytes + heapProfileRecordBytes
			for i := range count {
				offset := 8 + i*stride + bucketHeaderBytes + programCounterBytes
				record := raw[offset : offset+heapProfileRecordBytes]
				clear(record)
				if i == test.activeIndex {
					binary.LittleEndian.PutUint64(record, 1)
					binary.LittleEndian.PutUint64(record[16:], 128)
					if test.allFreed {
						binary.LittleEndian.PutUint64(record[8:], 1)
						binary.LittleEndian.PutUint64(record[24:], 128)
					}
				}
				if test.future {
					for base := 32; base < heapProfileRecordBytes; base += 32 {
						binary.LittleEndian.PutUint64(record[base:], 100)
						binary.LittleEndian.PutUint64(record[base+16:], 12800)
					}
				}
			}
			if test.cycle {
				binary.LittleEndian.PutUint64(raw[8+(count-1)*stride+8:], info.mbucketsHead)
			}
			result, err := (&processReader{memory: *memory, runtime: info}).scanHeapProfile(t.Context(), 1)
			if err != nil || result == nil {
				t.Fatalf("scan = %+v, %v", result, err)
			}
			if result.status != test.status || result.reason != test.reason ||
				len(result.allocations) != test.entries || result.hasOmittedAllocations {
				t.Fatalf("scan = %+v, want status=%s reason=%q entries=%d without truncation",
					result, test.status, test.reason, test.entries)
			}
			if test.entries != 0 {
				entry := result.allocations[0]
				if entry.inuseObjects != 1 || entry.inuseBytes != 128 ||
					binary.LittleEndian.Uint64([]byte(entry.key)) != uint64(test.activeIndex+1) {
					t.Fatalf("published allocation = %+v, want 1 object and 128 bytes from bucket %d", entry, test.activeIndex)
				}
			}
		})
	}
}

func TestScanHeapProfileKeyBudget(t *testing.T) {
	const depth = 1024
	const retained = maxAggregateKeyBytes / (depth * programCounterBytes)
	memory, info, raw := allocationFixture(t, retained+1, depth, retained+1)
	info.memProfileRate = 1
	reader := &processReader{memory: *memory, runtime: info}
	for _, test := range []struct {
		name   string
		next   uint64
		reason string
	}{
		{name: "budget", reason: fmt.Sprintf("aggregate stack-key memory limit %d bytes reached", maxAggregateKeyBytes)},
		{name: "cycle before budget", next: info.mbucketsHead, reason: "mbucket chain contains a cycle"},
	} {
		t.Run(test.name, func(t *testing.T) {
			// A cycle is detected before the last batch exhausts the aggregate budget.
			last := 8 + retained*(bucketHeaderBytes+depth*programCounterBytes+heapProfileRecordBytes)
			binary.LittleEndian.PutUint64(raw[last+8:], test.next)
			result, err := reader.scanHeapProfile(t.Context(), retained+1)
			if err != nil || result == nil {
				t.Fatalf("budgeted scan: result=%v err=%v", result, err)
			}
			if result.status != memsnapshot.SnapshotStatusPartial || result.hasOmittedAllocations ||
				len(result.allocations) != retained || result.reason != test.reason {
				t.Fatalf("status=%s hasOmittedAllocations=%v allocations=%d reason=%q, want reason %q",
					result.status, result.hasOmittedAllocations, len(result.allocations), result.reason, test.reason)
			}
		})
	}
}

func TestScanHeapProfileDeadline(t *testing.T) {
	memory, info, _ := bucketFixture(t, 1)
	reader := &processReader{memory: *memory, runtime: info}
	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(1, 0))
	defer cancel()
	if scan, err := reader.scanHeapProfile(ctx, 2); scan != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired scan: scan=%+v err=%v", scan, err)
	}
}

func TestScanHeapProfileCancellationDuringRead(t *testing.T) {
	memory, info, _ := bucketFixture(t, mbucketBatchSize+1)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	info.layout.byteOrder = cancelingByteOrder{ByteOrder: binary.LittleEndian, cancel: cancel}
	reader := &processReader{memory: *memory, runtime: info}
	if scan, err := reader.scanHeapProfile(ctx, 2); scan != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read: scan=%+v err=%v", scan, err)
	}
}

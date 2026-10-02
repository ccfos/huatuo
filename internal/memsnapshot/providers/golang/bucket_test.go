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
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func BenchmarkBucketBatch(b *testing.B) {
	for _, unique := range []int{1, 64} {
		b.Run(fmt.Sprintf("unique=%d", unique), func(b *testing.B) {
			var records [mbucketBatchSize][heapProfileRecordBytes]byte
			var stacks [mbucketBatchSize][8 * 8]byte
			var pin runtime.Pinner
			pin.Pin(&records[0][0])
			pin.Pin(&stacks[0][0])
			defer pin.Unpin()
			workspace := &bucketBatch{buckets: make([]bucketSample, mbucketBatchSize)}
			for i := range records {
				binary.LittleEndian.PutUint64(records[i][0:8], 1)
				binary.LittleEndian.PutUint64(records[i][16:24], 128)
				for j := 0; j < 8; j++ {
					binary.LittleEndian.PutUint64(stacks[i][j*8:], uint64(1+i%unique+j))
				}
				workspace.buckets[i] = bucketSample{
					recordAddress: uint64(uintptr(unsafe.Pointer(&records[i][0]))),
					stackAddress:  uint64(uintptr(unsafe.Pointer(&stacks[i][0]))),
					stackDepth:    8,
				}
			}
			memory := processMemory{pid: os.Getpid()}
			b.ReportAllocs()
			for b.Loop() {
				aggregates := newStackAggr()
				err := workspace.readRuntimeLayoutSamples(b.Context(), &memory,
					runtimeLayout{byteOrder: binary.LittleEndian, maxStackDepth: 1024})
				if err != nil {
					b.Fatalf("batch read failed: %v", err)
				}
				if addBatchSamplesForBenchmark(aggregates, workspace.buckets) {
					b.Fatal("aggregate budget exhausted")
				}
				if len(aggregates.indices) != unique {
					b.Fatalf("groups = %d, want %d", len(aggregates.indices), unique)
				}
			}
			runtime.KeepAlive(records)
			runtime.KeepAlive(stacks)
		})
	}
}

// The synthetic runtime lives in pinned memory, so the scanner exercises real
// process_vm_readv calls without coupling fixtures to the test runner's heap profile.
func bucketFixture(t testing.TB, count int) (*processMemory, *runtimeInfo, []byte) {
	return allocationFixture(t, count, 1, 2)
}

func allocationFixture(t testing.TB, count, depth, unique int) (*processMemory, *runtimeInfo, []byte) {
	t.Helper()
	stride := bucketHeaderBytes + depth*8 + heapProfileRecordBytes
	raw := make([]byte, 8+count*stride)
	pin := new(runtime.Pinner)
	pin.Pin(&raw[0])
	t.Cleanup(pin.Unpin)
	base := uint64(uintptr(unsafe.Pointer(&raw[0])))
	order := binary.LittleEndian
	if count > 0 {
		order.PutUint64(raw, base+8)
	}
	for i := 0; i < count; i++ {
		off := 8 + i*stride
		if i+1 < count {
			order.PutUint64(raw[off+8:], base+uint64(off+stride))
		}
		order.PutUint64(raw[off+16:], 1)
		order.PutUint64(raw[off+40:], uint64(depth))
		for j := 0; j < depth; j++ {
			order.PutUint64(raw[off+bucketHeaderBytes+j*8:], uint64(j+1))
		}
		if depth > 0 {
			// Make the final frame distinguish keys, including beyond the output limit.
			order.PutUint64(raw[off+bucketHeaderBytes+(depth-1)*8:], uint64(1+i%unique))
		}
		record := off + bucketHeaderBytes + depth*8
		order.PutUint64(raw[record:], 1)
		order.PutUint64(raw[record+16:], 128)
	}
	return &processMemory{pid: os.Getpid()}, &runtimeInfo{mbucketsHead: order.Uint64(raw[:8]), layout: runtimeLayout{byteOrder: order, maxStackDepth: 1024}, memProfileRate: 1}, raw
}

func bucketBatchFixture(t testing.TB, count, depth int) (*bucketBatch, *processMemory, runtimeLayout) {
	t.Helper()
	memory, info, _ := allocationFixture(t, count, depth, max(count, 1))
	batch := &bucketBatch{buckets: make([]bucketSample, count)}
	stride := bucketHeaderBytes + depth*programCounterBytes + heapProfileRecordBytes
	for i := range batch.buckets {
		stackAddress := info.mbucketsHead + uint64(i*stride+bucketHeaderBytes)
		batch.buckets[i] = bucketSample{
			recordAddress: stackAddress + uint64(depth*programCounterBytes),
			stackAddress:  stackAddress,
			stackDepth:    depth,
		}
	}
	return batch, memory, info.layout
}

func BenchmarkBucketBatchReadSamples(b *testing.B) {
	for _, depth := range []int{8, 1024} {
		b.Run(fmt.Sprintf("depth=%d", depth), func(b *testing.B) {
			batch, memory, layout := bucketBatchFixture(b, mbucketBatchSize, depth)
			// Exclude the first buffer allocation to measure steady-state reuse.
			if err := batch.readRuntimeLayoutSamples(b.Context(), memory, layout); err != nil {
				b.Fatalf("warm batch: %v", err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if err := batch.readRuntimeLayoutSamples(b.Context(), memory, layout); err != nil {
					b.Fatalf("read batch: %v", err)
				}
			}
		})
	}
}

func BenchmarkBucketBatchFirstRead(b *testing.B) {
	for _, depth := range []int{8, 1024} {
		b.Run(fmt.Sprintf("depth=%d", depth), func(b *testing.B) {
			fixture, memory, layout := bucketBatchFixture(b, mbucketBatchSize, depth)
			b.ReportAllocs()
			for b.Loop() {
				// Each snapshot owns its batch and first stack-buffer allocation.
				batch := &bucketBatch{buckets: append([]bucketSample(nil), fixture.buckets...)}
				if err := batch.readRuntimeLayoutSamples(b.Context(), memory, layout); err != nil {
					b.Fatalf("read first batch: %v", err)
				}
			}
		})
	}
}

func TestBucketBatchReadSamples(t *testing.T) {
	for _, test := range []struct {
		name         string
		count, depth int
	}{
		{name: "empty"},
		{name: "zero depth", count: 1},
		{name: "shallow stacks", count: mbucketBatchSize, depth: 8},
		{name: "deep stacks", count: mbucketBatchSize, depth: 1024},
	} {
		t.Run(test.name, func(t *testing.T) {
			batch, memory, layout := bucketBatchFixture(t, test.count, test.depth)
			for range 2 {
				err := batch.readRuntimeLayoutSamples(t.Context(), memory, layout)
				if err != nil {
					t.Fatalf("read batch: %v", err)
				}
				if test.depth == 0 && batch.stackStorage != nil {
					t.Fatal("allocated stack storage without any stack PCs")
				}
				for i := range batch.buckets {
					sample := &batch.buckets[i]
					if sample.objects != 1 || sample.bytes != 128 || len(sample.stack) != test.depth*programCounterBytes {
						t.Fatalf("sample %d: objects=%d bytes=%d stack bytes=%d", i, sample.objects, sample.bytes, len(sample.stack))
					}
					if test.depth > 0 && layout.byteOrder.Uint64(sample.stack[(test.depth-1)*programCounterBytes:]) != uint64(i+1) {
						t.Fatalf("sample %d lost its final frame", i)
					}
				}
			}
		})
	}
}

func TestBucketBatchReadStackPCsPreservesZeros(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		t.Run(order.String(), func(t *testing.T) {
			var stacks [3][3 * programCounterBytes]byte
			var pin runtime.Pinner
			pin.Pin(&stacks[0][0])
			defer pin.Unpin()
			batch := &bucketBatch{buckets: make([]bucketSample, len(stacks))}
			for i := range stacks {
				for j := range 3 {
					if j != i {
						order.PutUint64(stacks[i][j*programCounterBytes:], uint64(j+1))
					}
				}
				batch.buckets[i] = bucketSample{
					stackAddress: uint64(uintptr(unsafe.Pointer(&stacks[i][0]))),
					stackDepth:   3,
					objects:      1,
					bytes:        128,
				}
			}
			if err := batch.readStackPCs(&processMemory{pid: os.Getpid()}); err != nil {
				t.Fatalf("read stacks: %v", err)
			}
			for i := range stacks {
				if got := batch.buckets[i].stack; !bytes.Equal(got, stacks[i][:]) {
					t.Fatalf("stack with zero PC at %d = %x, want %x", i, got, stacks[i])
				}
			}
		})
	}
}

func TestBucketBatchReadSamplesFailure(t *testing.T) {
	for _, test := range []struct {
		name             string
		unreadableRecord bool
		unreadableStack  bool
		wantError        string
	}{
		{name: "record", unreadableRecord: true, wantError: "read mbucket records"},
		{name: "stack", unreadableStack: true, wantError: "read mbucket stacks"},
		{name: "record and stack", unreadableRecord: true, unreadableStack: true, wantError: "read mbucket records"},
	} {
		t.Run(test.name, func(t *testing.T) {
			batch, memory, layout := bucketBatchFixture(t, 3, 1)
			if err := batch.readRuntimeLayoutSamples(t.Context(), memory, layout); err != nil {
				t.Fatal(err)
			}
			if test.unreadableStack {
				batch.buckets[1].stackAddress = 1
			}
			if test.unreadableRecord {
				batch.buckets[1].recordAddress = 1
			}
			err := batch.readRuntimeLayoutSamples(t.Context(), memory, layout)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("batch error=%v, want %q", err, test.wantError)
			}
			for i := range batch.buckets {
				if batch.buckets[i].stack != nil {
					t.Fatalf("sample %d retained a stack after the batch failed", i)
				}
			}
		})
	}
}

func BenchmarkBucketBatchReadFailure(b *testing.B) {
	batch, memory, layout := bucketBatchFixture(b, 2, 1)
	batch.buckets[0].recordAddress = 1
	b.ReportAllocs()
	for b.Loop() {
		if err := batch.readRuntimeLayoutSamples(b.Context(), memory, layout); err == nil {
			b.Fatal("accepted an unreadable record")
		}
	}
}

func TestBucketBatchReuse(t *testing.T) {
	memory, info, raw := allocationFixture(t, 1, 1024, 1)
	workspace := &bucketBatch{buckets: make([]bucketSample, mbucketBatchSize)}
	workspace.buckets = workspace.buckets[:1]
	batch := workspace.buckets
	batch[0] = bucketSample{
		recordAddress: info.mbucketsHead + bucketHeaderBytes + 1024*programCounterBytes,
		stackAddress:  info.mbucketsHead + bucketHeaderBytes,
		stackDepth:    1024,
	}
	for i, live := range []bool{false, true, false} {
		record := raw[8+bucketHeaderBytes+1024*programCounterBytes:]
		clear(record)
		if live {
			binary.LittleEndian.PutUint64(record, 1)
			binary.LittleEndian.PutUint64(record[16:], 128)
		}
		err := workspace.readRuntimeLayoutSamples(t.Context(), memory, info.layout)
		if err != nil || (len(batch[0].stack) != 0) != live {
			t.Fatalf("live=%v stack=%d err=%v", live, len(batch[0].stack), err)
		}
		if i == 0 && workspace.stackStorage != nil {
			t.Fatal("allocated stack storage for an inactive sample")
		}
	}
	// Inactive deep stacks must not grow storage for their combined depths.
	storage := workspace.stackStorage
	workspace.buckets = workspace.buckets[:mbucketBatchSize]
	for i := range workspace.buckets {
		workspace.buckets[i] = batch[0]
	}
	err := workspace.readRuntimeLayoutSamples(t.Context(), memory, info.layout)
	if err != nil || cap(workspace.stackStorage) != cap(storage) ||
		unsafe.SliceData(workspace.stackStorage) != unsafe.SliceData(storage) {
		t.Fatalf("inactive deep stacks: %v", err)
	}
	binary.LittleEndian.PutUint64(raw[8+bucketHeaderBytes+1024*programCounterBytes:], 1)
	binary.LittleEndian.PutUint64(raw[8+bucketHeaderBytes+1024*programCounterBytes+16:], 128)
	workspace.buckets = workspace.buckets[:1]
	batch[0].stackAddress = 1
	err = workspace.readRuntimeLayoutSamples(t.Context(), memory, info.layout)
	if err == nil || !strings.Contains(err.Error(), "read mbucket stacks") || batch[0].stack != nil {
		t.Fatalf("unreadable stack: %v", err)
	}

	batch[0].recordAddress = 1
	err = workspace.readRuntimeLayoutSamples(t.Context(), memory, info.layout)
	if err == nil || !strings.Contains(err.Error(), "read mbucket records") || batch[0].stack != nil {
		t.Fatalf("unreadable record: %v", err)
	}
}

func TestBucketBatchStackStorageReuse(t *testing.T) {
	batch := &bucketBatch{}
	for _, depth := range []int{8, 1024, 8, 1024} {
		fixture, memory, layout := bucketBatchFixture(t, mbucketBatchSize, depth)
		batch.buckets = fixture.buckets
		previous := batch.stackStorage
		if err := batch.readRuntimeLayoutSamples(t.Context(), memory, layout); err != nil {
			t.Fatalf("depth=%d: %v", depth, err)
		}
		required := mbucketBatchSize * depth * programCounterBytes
		if cap(batch.stackStorage) < required {
			t.Fatalf("depth=%d: storage capacity=%d, need %d", depth, cap(batch.stackStorage), required)
		}
		if cap(previous) >= required && unsafe.SliceData(batch.stackStorage) != unsafe.SliceData(previous) {
			t.Fatalf("depth=%d: replaced sufficient storage", depth)
		}
		for i := range batch.buckets {
			stack := batch.buckets[i].stack
			if len(stack) != depth*programCounterBytes ||
				layout.byteOrder.Uint64(stack[(depth-1)*programCounterBytes:]) != uint64(i+1) {
				t.Fatalf("depth=%d: sample %d stack changed after buffer reuse", depth, i)
			}
		}
	}
}

func TestBucketBatchCancellation(t *testing.T) {
	for _, count := range []int{0, 1} {
		for _, expired := range []bool{false, true} {
			t.Run(fmt.Sprintf("buckets=%d/expired=%t", count, expired), func(t *testing.T) {
				memory, info, _ := bucketFixture(t, count)
				batch := &bucketBatch{}
				if count != 0 {
					batch.buckets = []bucketSample{{
						recordAddress: info.mbucketsHead + bucketHeaderBytes + programCounterBytes,
						stackAddress:  info.mbucketsHead + bucketHeaderBytes,
						stackDepth:    1,
					}}
				}
				ctx, cancel := context.WithCancel(t.Context())
				want := context.Canceled
				if expired {
					cancel()
					ctx, cancel = context.WithDeadline(t.Context(), time.Unix(1, 0))
					want = context.DeadlineExceeded
				}
				cancel()
				err := batch.readRuntimeLayoutSamples(ctx, memory, info.layout)
				if !errors.Is(err, want) {
					t.Fatalf("canceled batch: err=%v, want %v", err, want)
				}
				if count != 0 && batch.records[0] != ([heapProfileRecordBytes]byte{}) {
					t.Fatal("read records after cancellation")
				}
			})
		}
	}
}

func TestBucketBatchCancellationBeforeStackRead(t *testing.T) {
	memory, info, _ := bucketFixture(t, 1)
	batch := &bucketBatch{buckets: []bucketSample{{
		recordAddress: info.mbucketsHead + bucketHeaderBytes + programCounterBytes,
		stackAddress:  info.mbucketsHead + bucketHeaderBytes,
		stackDepth:    1,
	}}}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	info.layout.byteOrder = cancelingByteOrder{ByteOrder: binary.LittleEndian, cancel: cancel}
	err := batch.readRuntimeLayoutSamples(ctx, memory, info.layout)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled record decode: %v", err)
	}
	if batch.stackStorage != nil {
		t.Fatal("allocated stack storage after cancellation")
	}
}

func addBatchSamplesForBenchmark(aggregates *stackAggregates, buckets []bucketSample) bool {
	for i := range buckets {
		sample := &buckets[i]
		if len(sample.stack) != 0 && !aggregates.addSample(sample.stack, sample.objects, sample.bytes, 1) {
			return true
		}
	}
	return false
}

func TestRuntimeLayoutReadBucketHeader(t *testing.T) {
	var source [bucketHeaderBytes]byte
	for i := range source {
		source[i] = byte(i + 1)
	}
	var pin runtime.Pinner
	pin.Pin(&source[0])
	defer pin.Unpin()
	memory := processMemory{pid: os.Getpid()}
	address := uint64(uintptr(unsafe.Pointer(&source[0])))
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		layout := runtimeLayout{byteOrder: order}
		header, err := layout.readBucketHeader(&memory, address)
		if err != nil || header.raw != source {
			t.Fatalf("header=%+v err=%v", header, err)
		}
		original := source
		source[0]++
		next, err := layout.readBucketHeader(&memory, address)
		if err != nil || next.raw != source || header.raw != original {
			t.Fatalf("header storage was reused: first=%+v next=%+v err=%v", header, next, err)
		}
		if header, err := layout.readBucketHeader(&memory, 1); err == nil || header != (bucketHeader{}) {
			t.Fatalf("unreadable header=%+v err=%v", header, err)
		}
	}
	runtime.KeepAlive(source)
}

func TestRuntimeLayoutReadBucketHeaderShortRead(t *testing.T) {
	pageSize := os.Getpagesize()
	pages, err := unix.Mmap(-1, 0, 2*pageSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANON)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unix.Munmap(pages); err != nil {
			t.Error(err)
		}
	})
	if err := unix.Mprotect(pages[pageSize:], unix.PROT_NONE); err != nil {
		t.Fatal(err)
	}
	copy(pages[pageSize-8:], "trailing")
	memory := processMemory{pid: os.Getpid()}
	layout := runtimeLayout{byteOrder: binary.LittleEndian}
	address := uint64(uintptr(unsafe.Pointer(&pages[pageSize-8])))
	header, err := layout.readBucketHeader(&memory, address)
	if err == nil || header != (bucketHeader{}) {
		t.Fatalf("partial bytes escaped a failed read: header=%+v err=%v", header, err)
	}
}

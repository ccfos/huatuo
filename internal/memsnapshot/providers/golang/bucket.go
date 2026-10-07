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
	"fmt"
)

const mbucketBatchSize = maxProcessReadRanges

// readBucketHeader returns no bytes on failure, including a short remote read.
// Reusing the syscall destination avoids a heap allocation for every bucket.
func (l runtimeLayout) readBucketHeader(memory *processMemory, address uint64) (bucketHeader, error) {
	if err := memory.readInto(address, memory.headerRaw[:]); err != nil {
		return bucketHeader{}, err
	}

	return bucketHeader{raw: memory.headerRaw}, nil
}

type bucketSample struct {
	recordAddress uint64
	stackAddress  uint64
	stackDepth    int
	stack         []byte
	objects       uint64
	bytes         uint64
}

// bucketBatch reuses stack storage across batches, growing it only for active
// samples. Storage is bounded by 64 buckets * 1024 frames * 8 bytes.
type bucketBatch struct {
	buckets      []bucketSample
	records      [mbucketBatchSize][heapProfileRecordBytes]byte
	ranges       [mbucketBatchSize]remoteRange
	stackBuckets [mbucketBatchSize]int
	stackStorage []byte
}

func (batch *bucketBatch) appendBucket(d bucketDescriptor) {
	batch.buckets = append(batch.buckets, bucketSample{
		recordAddress: d.recordAddr,
		stackAddress:  d.stackAddr,
		stackDepth:    d.stackDepth,
	})
}

// readRuntimeLayoutSamples requires both read phases to succeed before samples can be used.
// Stacks borrow batch storage, so the caller must aggregate them before reuse.
func (batch *bucketBatch) readRuntimeLayoutSamples(ctx context.Context,
	memory *processMemory, layout runtimeLayout,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := batch.readMemRecords(memory, layout)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if err != nil {
		return err
	}

	err = batch.readStackPCs(memory)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}

	return err
}

func (batch *bucketBatch) readMemRecords(memory *processMemory, layout runtimeLayout) error {
	buckets := batch.buckets
	ranges := batch.ranges[:len(buckets)]
	for i := range buckets {
		bucket := &buckets[i]
		bucket.stack, bucket.objects, bucket.bytes = nil, 0, 0
		ranges[i] = remoteRange{address: bucket.recordAddress, data: batch.records[i][:]}
	}
	if err := memory.readBatch(ranges); err != nil {
		return fmt.Errorf("read mbucket records: %w", err)
	}

	for i := range buckets {
		bucket := &buckets[i]
		bucket.objects, bucket.bytes = layout.decodeCounters(&batch.records[i])
	}

	return nil
}

// readStackPCs uses the counters populated by readMemRecords to avoid reading
// stacks for inactive samples. Empty stacks need no process memory read.
func (batch *bucketBatch) readStackPCs(memory *processMemory) error {
	buckets := batch.buckets
	stackBytes := 0
	stackBuckets := batch.stackBuckets[:0]
	for i := range buckets {
		bucket := &buckets[i]
		if bucket.objects == 0 || bucket.bytes == 0 || bucket.stackDepth == 0 {
			continue
		}
		stackBytes += bucket.stackDepth * programCounterBytes
		stackBuckets = append(stackBuckets, i)
	}
	if len(stackBuckets) == 0 {
		return nil
	}

	if cap(batch.stackStorage) < stackBytes {
		batch.stackStorage = make([]byte, stackBytes)
	}
	stackStorage := batch.stackStorage[:stackBytes]
	// Record results have been consumed, so their ranges can be reused.
	ranges := batch.ranges[:len(stackBuckets)]
	for i, bucketIndex := range stackBuckets {
		bucket := &buckets[bucketIndex]
		size := bucket.stackDepth * programCounterBytes
		ranges[i] = remoteRange{address: bucket.stackAddress, data: stackStorage[:size]}
		stackStorage = stackStorage[size:]
	}
	if err := memory.readBatch(ranges); err != nil {
		return fmt.Errorf("read mbucket stacks: %w", err)
	}

	for i, bucketIndex := range stackBuckets {
		// runtime.bucket.stk uses nstk as the exact length, without a zero terminator.
		// The read range already has that length; truncating at zero would change the stack key.
		// https://github.com/golang/go/blob/go1.23.0/src/runtime/mprof.go#L247-L254
		buckets[bucketIndex].stack = ranges[i].data
	}

	return nil
}

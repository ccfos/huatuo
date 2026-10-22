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
	"slices"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

const maxVisitedBuckets = 262144

type scanResult struct {
	allocations           []allocation
	status                memsnapshot.Status
	reason                string
	hasOmittedAllocations bool
}

// scanHeapProfile consumes each batch before its borrowed stacks are reused.
// The caller supplies a validated positive topK; it bounds only the final result.
// Complete, partial, and unavailable scans return a result with no error.
// Partial scans retain the current batch's valid samples and the first reason.
// Execution failures, cancellation, and deadline expiry return an error with no result.
func (r *processReader) scanHeapProfile(ctx context.Context, topK int) (*scanResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	memory, info := &r.memory, r.runtime
	sampleRate := info.memProfileRate
	if sampleRate == 0 {
		return &scanResult{
			status: memsnapshot.SnapshotStatusUnavailable,
			reason: "Go heap profiling is disabled by MemProfileRate=0",
		}, nil
	}
	if sampleRate < 0 {
		return &scanResult{
			status: memsnapshot.SnapshotStatusUnavailable,
			reason: "runtime.MemProfileRate is unavailable",
		}, nil
	}

	if info.mbucketsHead == 0 {
		return &scanResult{
			status: memsnapshot.SnapshotStatusUnavailable,
			reason: "Go heap profile contains no buckets",
		}, nil
	}

	result := &scanResult{status: memsnapshot.SnapshotStatusComplete}
	layout := info.layout
	address := info.mbucketsHead
	// TopK bounds only the result. Reachable bucket stacks are read until
	// the aggregate-key budget is exhausted, so complete snapshots rank globally.
	aggregates := newStackAggr()
	// The count cap also bounds cycle-detection storage.
	visited := make(map[uint64]bool)
	// ByteOrder decoding can retain the header on the heap; reuse it across buckets.
	var header bucketHeader
	batch := &bucketBatch{buckets: make([]bucketSample, 0, mbucketBatchSize)}
	hasPublishedCounters := false
	for {
		batch.buckets = batch.buckets[:0]
		for address != 0 && len(batch.buckets) < mbucketBatchSize {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if visited[address] {
				result.reason = "mbucket chain contains a cycle"
				break
			}
			if len(visited) >= maxVisitedBuckets {
				result.reason = fmt.Sprintf("mbucket safety limit %d reached", maxVisitedBuckets)
				break
			}
			current := address
			var readErr error
			header, readErr = layout.readBucketHeader(memory, current)
			if readErr != nil {
				return nil, fmt.Errorf("read mbucket header %#x: %w", current, readErr)
			}
			visited[current] = true
			descriptor, err := layout.decodeBucketHeader(current, &header)
			if err != nil {
				result.reason = err.Error()
				break
			}

			batch.appendBucket(descriptor)

			address = descriptor.nextAddr
		}

		if err := batch.readRuntimeLayoutSamples(ctx, memory, layout); err != nil {
			return nil, err
		}
		hasPublishedCounters = hasPublishedCounters || batch.hasPublishedCounters
		for i := range batch.buckets {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			sample := &batch.buckets[i]
			if len(sample.stack) == 0 {
				continue
			}
			if aggregates.addSample(sample.stack, sample.objects, sample.bytes, sampleRate) {
				continue
			}
			if result.reason == "" {
				result.reason = fmt.Sprintf(
					"aggregate stack-key memory limit %d bytes reached", maxAggregateKeyBytes,
				)
			}
			break
		}
		// Finish this batch before stopping so valid samples survive a partial scan.
		if result.reason != "" || address == 0 {
			break
		}
	}
	if result.reason != "" {
		result.status = memsnapshot.SnapshotStatusPartial
	} else if !hasPublishedCounters {
		// Only a complete scan can establish that no published data was observed.
		result.status = memsnapshot.SnapshotStatusUnavailable
		result.reason = "Go heap profile has no published statistics"
	}

	var err error
	result.allocations, err = aggregates.sortedAllocations(ctx)
	if err != nil {
		return nil, err
	}
	if len(result.allocations) > topK {
		result.hasOmittedAllocations = true
		// Detach the prefix so discarded stacks and the full backing array can be collected.
		result.allocations = slices.Clone(result.allocations[:topK])
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	return result, nil
}

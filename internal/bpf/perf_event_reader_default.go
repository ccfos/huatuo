// Copyright 2025, 2026 The HuaTuo Authors
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

package bpf

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/ccfos/huatuo/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/perf"
)

// perfEventReader reads the eBPF perf_event_array.
type perfEventReader struct {
	done   <-chan struct{}
	rd     *perf.Reader
	cancel context.CancelFunc
}

// _ is a type assertion
var (
	_ PerfEventReader    = (*perfEventReader)(nil)
	_ PerfEventRawReader = (*perfEventReader)(nil)
)

// newPerfEventReader creates a new perfEventReader.
func newPerfEventReader(ctx context.Context, array *ebpf.Map, perCPUBufSize int) (PerfEventReader, error) {
	rd, err := perf.NewReader(array, perCPUBufSize)
	if err != nil {
		return nil, fmt.Errorf("create perf event reader: %w", err)
	}

	return newPerfEventReaderFromReader(ctx, rd), nil
}

func newPerfEventRawReader(
	ctx context.Context,
	array *ebpf.Map,
	opts PerfEventReaderOptions,
) (PerfEventRawReader, error) {
	perCPUBufferBytes, err := normalizePerfEventReaderOptions(opts, os.Getpagesize())
	if err != nil {
		return nil, err
	}

	rd, err := perf.NewReaderWithOptions(
		array,
		perCPUBufferBytes,
		perf.ReaderOptions{Watermark: int(opts.WatermarkBytes)},
	)
	if err != nil {
		return nil, fmt.Errorf("create raw perf event reader: %w", err)
	}

	return newPerfEventReaderFromReader(ctx, rd), nil
}

func newPerfEventReaderFromReader(ctx context.Context, rd *perf.Reader) *perfEventReader {
	readerCtx, cancel := context.WithCancel(ctx)
	return &perfEventReader{done: readerCtx.Done(), rd: rd, cancel: cancel}
}

// Close the perfEventReader.
func (r *perfEventReader) Close() error {
	r.cancel()
	return r.rd.Close()
}

const (
	readIntoPollTimeout = 100 * time.Millisecond

	// readBatchDeadline bounds how long ReadBatch waits for the first event of a
	// round. Once events start arriving, subsequent reads return quickly until the
	// rings are drained and the deadline fires again, ending the batch.
	readBatchDeadline = 500 * time.Millisecond
)

// ReadBatch drains all per-CPU ring buffers currently available and returns the
// parsed events and sample loss. It returns partial results with read or decode
// errors so callers can preserve progress.
func (r *perfEventReader) ReadBatch(newEvent func() any) (PerfEventBatch, error) {
	deadline := time.Now().Add(readBatchDeadline)

	var batch PerfEventBatch
	var rec perf.Record

	for {
		if err := r.readRecord(&rec, deadline); err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return batch, nil
			}

			return batch, err
		}

		if rec.LostSamples != 0 {
			batch.LostSamples += rec.LostSamples
			continue
		}

		dst := newEvent()
		if dst == nil {
			return batch, errors.New("perf event factory returned nil")
		}
		if err := decodePerfEvent(rec.RawSample, dst); err != nil {
			return batch, err
		}

		batch.Events = append(batch.Events, dst)
	}
}

// ReadInto reads the next eBPF perf event into dst.
func (r *perfEventReader) ReadInto(dst any) error {
	var record perf.Record

	for {
		err := r.readRecord(&record, time.Now().Add(readIntoPollTimeout))
		if errors.Is(err, os.ErrDeadlineExceeded) {
			continue
		}
		if err != nil {
			return err
		}

		if record.LostSamples != 0 {
			return &PerfEventSamplesLostError{Count: record.LostSamples}
		}

		return decodePerfEvent(record.RawSample, dst)
	}
}

const (
	perfEventHeaderSize       = 8
	perfEventRawSizeFieldSize = 4
	perfEventLostRecordSize   = 24
)

// ReadRawInto reads one variable-size sample or loss record without decoding
// it. RawSample storage is reused across calls with the same destination.
func (r *perfEventReader) ReadRawInto(dst *PerfEventRawRecord) error {
	if dst == nil {
		return errors.New("raw perf event destination is nil")
	}

	record := perf.Record{RawSample: dst.RawSample[:0]}
	if err := r.readRecord(&record, time.Time{}); err != nil {
		if errors.Is(err, perf.ErrFlushed) {
			return ErrPerfEventReaderFlushed
		}
		return err
	}

	setPerfEventRawRecord(dst, &record)
	return nil
}

func setPerfEventRawRecord(dst *PerfEventRawRecord, record *perf.Record) {
	dst.CPU = record.CPU
	dst.RawSample = record.RawSample
	dst.LostSamples = record.LostSamples
	dst.RemainingBytes = record.Remaining
	if record.LostSamples != 0 {
		dst.PerfRecordSize = perfEventLostRecordSize
		return
	}
	dst.PerfRecordSize = perfEventHeaderSize +
		perfEventRawSizeFieldSize + len(record.RawSample)
}

// Flush causes pending records to be returned before a distinguished result.
func (r *perfEventReader) Flush() error {
	if err := r.rd.Flush(); err != nil {
		return fmt.Errorf("flush perf event reader: %w", err)
	}
	return nil
}

// PerCPUBufferSize returns the effective perf data-ring capacity.
func (r *perfEventReader) PerCPUBufferSize() int {
	return r.rd.BufferSize()
}

func normalizePerfEventReaderOptions(opts PerfEventReaderOptions, pageSize int) (int, error) {
	if opts.PerCPUBufferBytes == 0 {
		return 0, errors.New("per-CPU perf buffer size must be positive")
	}
	if opts.WatermarkBytes == 0 {
		return 0, errors.New("perf event watermark must be positive")
	}
	if pageSize <= 0 {
		return 0, fmt.Errorf("invalid page size: %d", pageSize)
	}

	requested := uint64(opts.PerCPUBufferBytes)
	pageBytes := uint64(pageSize)
	pages := (requested + pageBytes - 1) / pageBytes
	capacityPages := uint64(1)
	for capacityPages < pages {
		capacityPages <<= 1
	}

	capacity := capacityPages * pageBytes
	maxInt := uint64(^uint(0) >> 1)
	if capacity > maxInt {
		return 0, fmt.Errorf(
			"effective per-CPU perf buffer size %d overflows int",
			capacity,
		)
	}
	if uint64(opts.WatermarkBytes) >= capacity {
		return 0, fmt.Errorf(
			"perf event watermark %d must be smaller than effective per-CPU buffer size %d",
			opts.WatermarkBytes,
			capacity,
		)
	}

	return int(capacity), nil
}

func (r *perfEventReader) readRecord(record *perf.Record, deadline time.Time) error {
	select {
	case <-r.done:
		return types.ErrExitByCancelCtx
	default:
	}

	r.rd.SetDeadline(deadline)
	if err := r.rd.ReadInto(record); err != nil {
		if errors.Is(err, perf.ErrClosed) {
			return types.ErrExitByCancelCtx
		}

		return fmt.Errorf("read perf event: %w", err)
	}

	return nil
}

func decodePerfEvent(sample []byte, dst any) error {
	if _, err := binary.Decode(sample, binary.NativeEndian, dst); err != nil {
		return fmt.Errorf("parse perf event: %w", err)
	}

	return nil
}

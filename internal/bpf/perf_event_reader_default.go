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
var _ PerfEventReader = (*perfEventReader)(nil)

// newPerfEventReader creates a new perfEventReader.
func newPerfEventReader(ctx context.Context, array *ebpf.Map, perCPUBufSize int) (PerfEventReader, error) {
	rd, err := perf.NewReader(array, perCPUBufSize)
	if err != nil {
		return nil, fmt.Errorf("create perf event reader: %w", err)
	}

	readerCtx, cancel := context.WithCancel(ctx)
	return &perfEventReader{done: readerCtx.Done(), rd: rd, cancel: cancel}, nil
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

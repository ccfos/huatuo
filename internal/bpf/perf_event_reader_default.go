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

	"huatuo-bamai/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/perf"
)

var errInvalidPerfEventFactory = errors.New("invalid perf event factory")

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

// readBatchDeadline bounds how long ReadBatch waits for the first event of a
// round. Once events start arriving, subsequent reads return quickly until the
// rings are drained and the deadline fires again, ending the batch.
const readBatchDeadline = 500 * time.Millisecond

// ReadBatch drains all per-CPU ring buffers currently available and returns the
// parsed events. It returns any decoded events with read, decode, or loss
// errors so callers can preserve partial progress.
func (r *perfEventReader) ReadBatch(newEvent func() any) ([]any, error) {
	select {
	case <-r.done:
		return nil, types.ErrExitByCancelCtx
	default:
	}

	if newEvent == nil {
		return nil, fmt.Errorf(
			"%w: factory required", errInvalidPerfEventFactory,
		)
	}

	r.rd.SetDeadline(time.Now().Add(readBatchDeadline))

	var batch []any
	var rec perf.Record
	var lostSamples uint64

	for {
		if err := r.rd.ReadInto(&rec); err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return batch, newPerfEventSamplesLostError(lostSamples)
			}

			return batch, errors.Join(
				normalizePerfReadError(err),
				newPerfEventSamplesLostError(lostSamples),
			)
		}

		if rec.LostSamples != 0 {
			lostSamples += rec.LostSamples
			continue
		}

		dst := newEvent()
		if dst == nil {
			return batch, errors.Join(
				fmt.Errorf(
					"%w: factory returned nil", errInvalidPerfEventFactory,
				),
				newPerfEventSamplesLostError(lostSamples),
			)
		}
		if err := decodePerfEvent(rec.RawSample, dst); err != nil {
			return batch, errors.Join(
				err,
				newPerfEventSamplesLostError(lostSamples),
			)
		}

		batch = append(batch, dst)
	}
}

// ReadInto reads the next eBPF perf event into dst.
func (r *perfEventReader) ReadInto(dst any) error {
	for {
		select {
		case <-r.done:
			return types.ErrExitByCancelCtx
		default:
			// set the poll deadline 100ms
			r.rd.SetDeadline(time.Now().Add(100 * time.Millisecond))

			// read the event
			record, err := r.rd.Read()
			if err != nil {
				if errors.Is(err, os.ErrDeadlineExceeded) { // poll deadline
					continue
				}

				return normalizePerfReadError(err)
			}

			if record.LostSamples != 0 {
				return newPerfEventSamplesLostError(record.LostSamples)
			}

			if err := decodePerfEvent(record.RawSample, dst); err != nil {
				return err
			}

			return nil
		}
	}
}

func decodePerfEvent(sample []byte, dst any) error {
	if _, err := binary.Decode(sample, binary.NativeEndian, dst); err != nil {
		return fmt.Errorf("parse perf event: %w", err)
	}

	return nil
}

func newPerfEventSamplesLostError(count uint64) error {
	if count == 0 {
		return nil
	}

	return &PerfEventSamplesLostError{Count: count}
}

func normalizePerfReadError(err error) error {
	if errors.Is(err, perf.ErrClosed) {
		return types.ErrExitByCancelCtx
	}

	return fmt.Errorf("read perf event: %w", err)
}

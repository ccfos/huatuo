// Copyright 2025 The HuaTuo Authors
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

//go:build !didi

package bpf

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"reflect"
	"time"

	"huatuo-bamai/pkg/types"

	"github.com/cilium/ebpf"
	"github.com/cilium/ebpf/perf"
	"github.com/pkg/errors"
)

// perfEventReader reads the eBPF perf_event_array.
type perfEventReader struct {
	ctx       context.Context
	rd        *perf.Reader
	cancelCtx context.CancelFunc
}

// _ is a type assertion
var _ PerfEventReader = (*perfEventReader)(nil)

// newPerfEventReader creates a new perfEventReader.
func newPerfEventReader(ctx context.Context, array *ebpf.Map, perCPUBufSize int) (PerfEventReader, error) {
	rd, err := perf.NewReader(array, perCPUBufSize)
	if err != nil {
		return nil, fmt.Errorf("can't create the perf event reader: %w", err)
	}

	readerCtx, cancel := context.WithCancel(ctx)
	return &perfEventReader{ctx: readerCtx, rd: rd, cancelCtx: cancel}, nil
}

// Close the perfEventReader.
func (r *perfEventReader) Close() error {
	r.cancelCtx()
	return r.rd.Close()
}

// readBatchDeadline bounds how long ReadBatch waits for the first event of a
// round. Once events start arriving, subsequent reads return quickly until the
// rings are drained and the deadline fires again, ending the batch.
const readBatchDeadline = 500 * time.Millisecond

// ReadBatch drains all per-CPU ring buffers currently available and returns the
// parsed events. It sets a deadline, then loops ReadInto until the deadline
// fires (os.ErrDeadlineExceeded), which signals no more data is ready this
// round. Each returned element is a newly allocated value of pdata's type.
func (r *perfEventReader) ReadBatch(pdata any) ([]any, error) {
	select {
	case <-r.ctx.Done():
		return nil, types.ErrExitByCancelCtx
	default:
	}

	elemType := reflect.TypeOf(pdata)
	if elemType.Kind() == reflect.Ptr {
		elemType = elemType.Elem()
	}

	r.rd.SetDeadline(time.Now().Add(readBatchDeadline))

	var batch []any
	var rec perf.Record

	for {
		if err := r.rd.ReadInto(&rec); err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return batch, nil
			}
			if errors.Is(err, perf.ErrClosed) {
				return batch, fmt.Errorf("perfEventReader is closed: %w", types.ErrExitByCancelCtx)
			}
			return nil, fmt.Errorf("failed to read the event: %w", err)
		}

		if rec.LostSamples != 0 {
			continue
		}

		dst := reflect.New(elemType).Interface()
		if err := binary.Read(bytes.NewBuffer(rec.RawSample), binary.NativeEndian, dst); err != nil {
			return nil, fmt.Errorf("failed to parse the event: %w", err)
		}

		batch = append(batch, dst)
	}
}

// ReadInto reads the eBPF perf_event into pdata.
func (r *perfEventReader) ReadInto(pdata any) error {
	for {
		select {
		case <-r.ctx.Done():
			return types.ErrExitByCancelCtx
		default:
			// set the poll deadline 100ms
			r.rd.SetDeadline(time.Now().Add(100 * time.Millisecond))

			// read the event
			record, err := r.rd.Read()
			if err != nil {
				if errors.Is(err, perf.ErrClosed) { // Close
					return fmt.Errorf("perfEventReader is closed: %w", types.ErrExitByCancelCtx)
				} else if errors.Is(err, os.ErrDeadlineExceeded) { // poll deadline
					continue
				}
				return fmt.Errorf("failed to read the event: %w", err)
			}

			if record.LostSamples != 0 {
				continue
			}

			// parse the event
			if err := binary.Read(bytes.NewBuffer(record.RawSample), binary.NativeEndian, pdata); err != nil {
				return fmt.Errorf("failed to parse the event: %w", err)
			}

			return nil
		}
	}
}

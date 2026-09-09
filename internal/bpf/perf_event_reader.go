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
	"errors"
	"fmt"
)

// ErrPerfEventSamplesLost indicates that the kernel dropped perf samples.
var ErrPerfEventSamplesLost = errors.New("bpf: perf event samples lost")

// ErrPerfEventReaderFlushed indicates that a raw perf event reader has
// returned every record which was pending when Flush was called.
var ErrPerfEventReaderFlushed = errors.New("bpf: perf event reader flushed")

// PerfEventSamplesLostError reports how many perf samples the kernel dropped.
type PerfEventSamplesLostError struct {
	Count uint64
}

func (e *PerfEventSamplesLostError) Error() string {
	return fmt.Sprintf("bpf: %d perf event samples lost", e.Count)
}

func (e *PerfEventSamplesLostError) Unwrap() error {
	return ErrPerfEventSamplesLost
}

// PerfEventBatch contains decoded events and sample loss observed during one
// ReadBatch call. Events remain valid when ReadBatch returns an error.
type PerfEventBatch struct {
	Events      []any
	LostSamples uint64
}

// PerfEventReaderOptions configures a raw perf event reader.
type PerfEventReaderOptions struct {
	// PerCPUBufferBytes is the requested data-ring capacity for each CPU. The
	// effective capacity is rounded up to a power-of-two number of pages.
	PerCPUBufferBytes uint32

	// WatermarkBytes is the unread-byte count which wakes the reader. It must be
	// positive and smaller than the effective per-CPU data-ring capacity.
	WatermarkBytes uint32
}

// PerfEventRawRecord contains one raw perf-ring record.
type PerfEventRawRecord struct {
	CPU int

	// RawSample is the data submitted through bpf_perf_event_output. It is empty
	// for a PERF_RECORD_LOST record and remains valid until the next read.
	RawSample []byte

	// LostSamples is nonzero for a PERF_RECORD_LOST record.
	LostSamples uint64

	// RemainingBytes is the unread data remaining in the source CPU's ring after
	// this record. PerfRecordSize includes perf framing around this record.
	RemainingBytes int
	PerfRecordSize int
}

// PerfEventReader reads the eBPF perf_event.
type PerfEventReader interface {
	// ReadInto reads the next eBPF perf event into dst. Sample loss returns
	// ErrPerfEventSamplesLost and may be retried.
	ReadInto(dst any) error

	// ReadBatch drains all per-CPU ring buffers currently available within a
	// bounded deadline. newEvent must return a new event destination per call.
	// The returned batch may contain events and sample loss when err is non-nil.
	ReadBatch(newEvent func() any) (PerfEventBatch, error)

	// Close the PerfEventReader.
	Close() error
}

// PerfEventRawReader reads variable-size records without decoding them.
type PerfEventRawReader interface {
	// ReadRawInto blocks until one sample or loss record is available. Flush
	// causes pending records to be returned before ErrPerfEventReaderFlushed.
	ReadRawInto(dst *PerfEventRawRecord) error

	// Flush wakes the reader and limits subsequent reads to records already
	// pending when Flush was called.
	Flush() error

	// PerCPUBufferSize returns the effective data-ring capacity for each CPU. It
	// excludes the perf metadata page.
	PerCPUBufferSize() int

	// Close the PerfEventRawReader.
	Close() error
}

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
	"time"
)

var (
	// ErrPerfEventSamplesLost indicates that the kernel dropped perf samples.
	ErrPerfEventSamplesLost = errors.New("bpf: perf event samples lost")
	// ErrPerfFlushed marks the boundary requested by PerfEventReader.Flush.
	ErrPerfFlushed = errors.New("bpf: perf event reader flushed")
)

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

// PerfEventReader reads the eBPF perf_event.
type PerfEventReader interface {
	// ReadInto reads the next eBPF perf event into dst. Sample loss returns
	// ErrPerfEventSamplesLost and may be retried.
	ReadInto(dst any) error

	// ReadBatch drains all per-CPU ring buffers currently available within a
	// bounded deadline. newEvent must return a new event destination per call.
	ReadBatch(newEvent func() any) ([]any, error)

	// PollInto waits for at most timeout and decodes one event into dst. A
	// deadline with no event is reported as (false, nil).
	PollInto(dst any, timeout time.Duration) (bool, error)

	// Flush makes the reader return records already queued in the perf rings,
	// followed by ErrPerfFlushed.
	Flush() error

	// Close the PerfEventReader.
	Close() error
}

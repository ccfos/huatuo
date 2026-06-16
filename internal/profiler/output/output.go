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

// Package output defines a unified Formatter interface for profiling output.
// Supports both streaming (Count=1, Timestamp set) and batch (Count>1) usage.
package output

import "io"

// Frame holds optional per-frame metadata for rich output formats.
// When FrameDetails is populated, FrameDetails[i] corresponds to Frames[i].
type Frame struct {
	File string // source file path; empty = unknown
	Line int32  // source line number; 0 = unknown
}

// Sample is the common profiling data unit.
type Sample struct {
	// Frames is the call stack, outermost (caller) first.
	Frames []string

	// FrameDetails carries optional file/line metadata parallel to Frames.
	// When non-nil, len(FrameDetails) must equal len(Frames).
	// Formatters that support it (speedscope) use this for source navigation.
	FrameDetails []Frame

	// Count is the number of occurrences. 1 for streaming, >1 for pre-aggregated.
	Count int64

	// ThreadID identifies the goroutine/thread. Used by chrometrace and speedscope.
	ThreadID string

	// ThreadName is the human-readable name of the thread.
	ThreadName string

	// PID is the OS process ID. Zero means unknown.
	PID int

	// Timestamp is the sample wall-clock time in Unix microseconds. Zero = unknown.
	Timestamp int64

	// Tags carries arbitrary key-value metadata.
	// chrometrace treats samples with no Frames but non-empty Tags as counter events.
	Tags map[string]string
}

// Formatter accumulates samples and serializes them to a specific output format.
type Formatter interface {
	Name() string
	Add(s *Sample) error
	Write(w io.Writer) error
	Reset()
}

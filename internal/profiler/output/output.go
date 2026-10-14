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

// Package output formats common profiling windows and renders local samples.
package output

import (
	"errors"
	"fmt"
	"io"
	"sync"
)

// OutputFormat enumerates the supported profiling output formats.
// The zero value "" is treated as FormatCollapsed.
type OutputFormat string

const (
	FormatCollapsed   OutputFormat = "collapsed"
	FormatFlameGraph  OutputFormat = "flamegraph"
	FormatSVG         OutputFormat = "svg"
	FormatPprof       OutputFormat = "pprof" // reserved; not yet implemented
	FormatJSON        OutputFormat = "json"
	FormatRemoteStore OutputFormat = "remote-store" // upload to a remote storage backend
)

// IsFlameGraph reports whether the format renders a flame graph (flamegraph or svg).
func (f OutputFormat) IsFlameGraph() bool {
	return f == FormatFlameGraph || f == FormatSVG
}

var (
	rendererFactories = map[OutputFormat]func() Renderer{}
	rendererMu        sync.RWMutex
)

// RegisterRenderer registers a factory for the given output format.
// Called by sub-packages in init().
func RegisterRenderer(f OutputFormat, fn func() Renderer) {
	rendererMu.Lock()
	rendererFactories[f] = fn
	rendererMu.Unlock()
}

// ErrUnregisteredFormat indicates the requested output format has no
// registered factory (the corresponding sub-package was not imported).
var ErrUnregisteredFormat = errors.New("output: format not registered")

// NewRenderer returns the appropriate Renderer for this format.
// The corresponding sub-package must be imported so its init() registers
// the factory; otherwise ErrUnregisteredFormat is returned.
func (f OutputFormat) NewRenderer() (Renderer, error) {
	if f == "" {
		f = FormatCollapsed
	}

	rendererMu.RLock()
	fn, ok := rendererFactories[f]
	rendererMu.RUnlock()

	if ok {
		return fn(), nil
	}

	return nil, fmt.Errorf("%w: %q", ErrUnregisteredFormat, f)
}

// Frame holds optional per-frame metadata for rich output formats.
// When FrameDetails is populated, FrameDetails[i] corresponds to Frames[i].
type Frame struct {
	File string // source file path; empty = unknown
	Line int32  // source line number; 0 = unknown
}

// Sample is the common profiling data unit.
type Sample struct {
	// Frames is the call stack, outermost (caller) first. Frame names are raw;
	// formatters apply any escaping or normalization required by their format.
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

// Renderer implements sample-level serialization behind a window Formatter.
type Renderer interface {
	Add(s *Sample) error
	Write(w io.Writer) error
	IsEmpty() bool
}

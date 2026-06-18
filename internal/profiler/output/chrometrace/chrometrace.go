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

// Package chrometrace writes Chrome Trace Event JSON, loadable in chrome://tracing or Perfetto.
package chrometrace

import (
	"encoding/json"
	"io"
	"strconv"

	"huatuo-bamai/internal/profiler/output"
)

type event struct {
	Name string         `json:"name"`
	Cat  string         `json:"cat,omitempty"`
	Ph   string         `json:"ph"` // "B", "E", "X", "M", or "C"
	PID  int            `json:"pid"`
	TID  string         `json:"tid"`
	TS   float64        `json:"ts"`            // microseconds
	Dur  float64        `json:"dur,omitempty"` // X events only
	Args map[string]any `json:"args,omitempty"`
}

type traceOutput struct {
	TraceEvents []event `json:"traceEvents"`
}

// prevState tracks the last-seen stack for one thread.
type prevState struct {
	frames []string
	pid    int
}

// nestingIndent is the microsecond offset between nested X events,
// giving visual depth in trace viewers.
const nestingIndent = 0.001

// Formatter accumulates samples and writes Chrome Trace JSON.
type Formatter struct {
	events       []event
	prev         map[string]*prevState // threadID → last state (streaming)
	seenThreads  map[string]struct{}   // threads that already have an M event
	sampleIdx    int
	sampleRateHz float64
}

var _ output.Formatter = (*Formatter)(nil)

// New creates a Formatter. Pass 0 for sampleRateHz to default to 100 Hz.
func New(sampleRateHz float64) *Formatter {
	if sampleRateHz <= 0 {
		sampleRateHz = 100
	}
	return &Formatter{
		prev:         make(map[string]*prevState),
		seenThreads:  make(map[string]struct{}),
		sampleRateHz: sampleRateHz,
	}
}

func (f *Formatter) Name() string { return "chrometrace" }

// Add incorporates one sample.
//
// Streaming (Count=1, ThreadID set): diffs stacks per thread, emits B/E events.
// Batch (Count>1 or no ThreadID): emits X events with duration ∝ Count.
// Counter (no Frames, Tags non-empty): emits a C event for each tag value.
//
// The first sample from each thread also emits an M (thread_name) metadata event.
func (f *Formatter) Add(s *output.Sample) error {
	ts := f.timestampFor(s)

	// M event: name the thread on first encounter.
	if s.ThreadID != "" {
		if _, seen := f.seenThreads[s.ThreadID]; !seen {
			f.seenThreads[s.ThreadID] = struct{}{}
			name := s.ThreadName
			if name == "" {
				name = s.ThreadID
			}

			f.events = append(f.events, event{
				Name: "thread_name",
				Ph:   "M",
				PID:  s.PID,
				TID:  s.ThreadID,
				Args: map[string]any{"name": name},
			})
		}
	}

	// C event: samples with no frames but non-empty Tags are counter observations.
	if len(s.Frames) == 0 {
		if len(s.Tags) > 0 {
			f.events = append(f.events, counterEvent(s, ts))
		}

		f.sampleIdx++

		return nil
	}

	if s.Count <= 1 && s.ThreadID != "" {
		f.addStreaming(s, ts)
	} else {
		f.addBatch(s, ts)
	}

	f.sampleIdx++

	return nil
}

// counterEvent builds a C (counter) event from sample tags.
// Numeric tag values are stored as float64; others remain strings.
func counterEvent(s *output.Sample, ts float64) event {
	args := make(map[string]any, len(s.Tags))

	for k, v := range s.Tags {
		if fv, err := strconv.ParseFloat(v, 64); err == nil {
			args[k] = fv
		} else {
			args[k] = v
		}
	}

	name := "counters"
	if s.ThreadName != "" {
		name = s.ThreadName
	}

	return event{
		Name: name,
		Ph:   "C",
		PID:  s.PID,
		TID:  s.ThreadID,
		TS:   ts,
		Args: args,
	}
}

func (f *Formatter) addStreaming(s *output.Sample, ts float64) {
	tid := s.ThreadID
	prev := f.prev[tid]

	var oldFrames []string
	if prev != nil {
		oldFrames = prev.frames
	}
	newFrames := make([]string, len(s.Frames))
	copy(newFrames, s.Frames)

	common := commonSuffixLen(oldFrames, newFrames)

	for i := range len(oldFrames) - common {
		f.events = append(f.events, event{
			Name: oldFrames[i],
			Cat:  "profiler",
			Ph:   "E",
			PID:  s.PID,
			TID:  tid,
			TS:   ts,
		})
	}

	// Emit "B" for frames that are entering (outermost-first).
	beginCount := len(newFrames) - common
	for i := beginCount - 1; i >= 0; i-- {
		f.events = append(f.events, event{
			Name: newFrames[i],
			Cat:  "profiler",
			Ph:   "B",
			PID:  s.PID,
			TID:  tid,
			TS:   ts,
		})
	}

	if prev == nil {
		prev = &prevState{}
		f.prev[tid] = prev
	}
	prev.frames = newFrames
	prev.pid = s.PID
}

// addBatch emits X events; each frame gets a duration proportional to Count.
func (f *Formatter) addBatch(s *output.Sample, ts float64) {
	tid := s.ThreadID
	if tid == "" {
		tid = "main"
	}
	dur := float64(s.Count) * (1e6 / f.sampleRateHz) // microseconds

	// Emit outermost frame as parent, innermost as deepest child.
	// Use nested X events by staggering start times slightly.
	offset := 0.0

	for i := range len(s.Frames) {
		f.events = append(f.events, event{
			Name: s.Frames[i],
			Cat:  "profiler",
			Ph:   "X",
			PID:  s.PID,
			TID:  tid,
			TS:   ts + offset,
			Dur:  dur - offset,
		})
		offset += nestingIndent
	}
}

func (f *Formatter) Write(w io.Writer) error {
	finalTS := f.timestampFor(&output.Sample{})

	n := len(f.events)
	for _, ps := range f.prev {
		n += len(ps.frames)
	}

	all := make([]event, 0, n)
	all = append(all, f.events...)

	for tid, ps := range f.prev {
		for _, name := range ps.frames {
			all = append(all, event{
				Name: name,
				Cat:  "profiler",
				Ph:   "E",
				PID:  ps.pid,
				TID:  tid,
				TS:   finalTS,
			})
		}
	}

	return json.NewEncoder(w).Encode(traceOutput{TraceEvents: all})
}

func (f *Formatter) Reset() {
	f.events = nil
	f.prev = make(map[string]*prevState)
	f.seenThreads = make(map[string]struct{})
	f.sampleIdx = 0
}

func (f *Formatter) IsEmpty() bool {
	return len(f.events) == 0
}

func (f *Formatter) timestampFor(s *output.Sample) float64 {
	if s.Timestamp != 0 {
		return float64(s.Timestamp) // already in microseconds
	}
	return float64(f.sampleIdx) * 1e6 / f.sampleRateHz
}

// commonSuffixLen returns the length of the shared outermost suffix.
func commonSuffixLen(a, b []string) int {
	i, j := len(a)-1, len(b)-1
	n := 0
	for i >= 0 && j >= 0 && a[i] == b[j] {
		n++
		i--
		j--
	}
	return n
}

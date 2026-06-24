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

// Package dump writes stack traces as human-readable text or JSON.
package dump

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"huatuo-bamai/internal/profiler/output"
)

// Options configures the dump output.
type Options struct {
	JSON      bool   // emit JSON array instead of text
	ShowCount bool   // include sample count in header
	Indent    string // per-frame indent (default: 4 spaces)
}

type dumpEntry struct {
	ThreadID   string   `json:"thread_id,omitempty"`
	ThreadName string   `json:"thread_name,omitempty"`
	PID        int      `json:"pid,omitempty"`
	Count      int64    `json:"count"`
	Frames     []string `json:"frames"`
}

// Formatter accumulates samples and writes a stack dump.
type Formatter struct {
	opts    Options
	samples []output.Sample
}

var _ output.Formatter = (*Formatter)(nil)

// New returns a Formatter with the given options. Indent defaults to 4 spaces.
func New(opts Options) *Formatter {
	if opts.Indent == "" {
		opts.Indent = "    "
	}

	return &Formatter{opts: opts}
}

func (f *Formatter) Name() string { return "dump" }

func (f *Formatter) Add(s *output.Sample) error {
	if len(s.Frames) > 0 {
		f.samples = append(f.samples, *s)
	}
	return nil
}

func (f *Formatter) Write(w io.Writer) error {
	if f.opts.JSON {
		return f.writeJSON(w)
	}
	return f.writeText(w)
}

func (f *Formatter) Reset() {
	f.samples = nil
}

func (f *Formatter) IsEmpty() bool {
	return len(f.samples) == 0
}

func (f *Formatter) writeText(w io.Writer) error {
	for i := range f.samples {
		if err := f.writeOneSampleText(w, &f.samples[i]); err != nil {
			return err
		}
	}
	return nil
}

func (f *Formatter) writeOneSampleText(w io.Writer, s *output.Sample) error {
	parts := []string{"Thread"}

	if s.ThreadID != "" {
		parts = append(parts, s.ThreadID)
	} else {
		parts = append(parts, "unknown")
	}
	if s.PID != 0 {
		parts = append(parts, fmt.Sprintf("(pid=%d)", s.PID))
	}
	if s.ThreadName != "" {
		parts = append(parts, fmt.Sprintf("%q", s.ThreadName))
	}
	if f.opts.ShowCount {
		parts = append(parts, fmt.Sprintf("[count=%d]", s.Count))
	}
	if _, err := fmt.Fprintln(w, strings.Join(parts, " ")); err != nil {
		return err
	}
	for _, frame := range s.Frames {
		if _, err := fmt.Fprintln(w, f.opts.Indent+frame); err != nil {
			return err
		}
	}

	_, err := fmt.Fprintln(w)
	return err
}

func (f *Formatter) writeJSON(w io.Writer) error {
	entries := make([]dumpEntry, 0, len(f.samples))
	for i := range f.samples {
		s := &f.samples[i]
		entries = append(entries, dumpEntry{
			ThreadID:   s.ThreadID,
			ThreadName: s.ThreadName,
			PID:        s.PID,
			Count:      s.Count,
			Frames:     s.Frames,
		})
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(entries)
}

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

// Package raw writes folded-stack format (Brendan Gregg): "frame;frame count\n".
package raw

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ccfos/huatuo/internal/profiler/output"
)

// Formatter writes folded-stack output.
type Formatter struct {
	counts map[string]int64
}

var _ output.Formatter = (*Formatter)(nil)

func init() {
	output.RegisterFormatter(output.FormatCollapsed, func() output.Formatter { return New() })
}

// New returns a Formatter that writes folded-stack output.
func New() *Formatter {
	return &Formatter{counts: make(map[string]int64)}
}

func (f *Formatter) Name() string { return "raw" }

func (f *Formatter) Add(s *output.Sample) error {
	if len(s.Frames) == 0 {
		return nil
	}
	key := foldedStackKey(s.Frames)
	f.counts[key] += s.Count
	if f.counts[key] == 0 {
		// Zero-count stacks have no visual weight and make empty profiles appear non-empty.
		// Do not remove this deletion unless zero-count stacks become part of the output contract.
		delete(f.counts, key)
	}
	return nil
}

func foldedStackKey(frames []string) string {
	if len(frames) == 0 {
		return ""
	}

	length := len(frames) - 1
	for _, frame := range frames {
		length += len(frame)
	}

	var key strings.Builder
	key.Grow(length)
	for index, frame := range frames {
		if index > 0 {
			key.WriteByte(';')
		}
		writeFoldedFrame(&key, frame)
	}
	return key.String()
}

func writeFoldedFrame(key *strings.Builder, frame string) {
	start := 0
	for index := range len(frame) {
		var replacement byte
		switch frame[index] {
		case ';':
			replacement = ':'
		case '\r', '\n':
			replacement = ' '
		default:
			continue
		}

		key.WriteString(frame[start:index])
		key.WriteByte(replacement)
		start = index + 1
	}
	key.WriteString(frame[start:])
}

func (f *Formatter) Write(w io.Writer) error {
	keys := make([]string, 0, len(f.counts))

	for k, count := range f.counts {
		// Net-negative and zero-count stacks have no visual weight in
		// folded output; negative counts come from physical_usage free
		// events, which the pprof path also drops when a stack nets
		// negative. Filtering here (not in Add) keeps every running sum
		// exact, so a stack that nets positive still shows its full net.
		if count <= 0 {
			continue
		}
		keys = append(keys, k)
	}

	sort.Strings(keys)

	for _, k := range keys {
		if _, err := fmt.Fprintf(w, "%s %d\n", k, f.counts[k]); err != nil {
			return err
		}
	}

	return nil
}

func (f *Formatter) Reset() {
	f.counts = make(map[string]int64)
}

// IsEmpty reports whether the formatter contains no visible samples.
// Stacks with a non-positive net count are dropped from output, so they do
// not count as samples here either.
func (f *Formatter) IsEmpty() bool {
	for _, count := range f.counts {
		if count > 0 {
			return false
		}
	}
	return true
}

// Counts returns a copy of the accumulated stack-to-count map, keeping only
// stacks with a positive net count; see Write for why non-positive stacks
// are excluded.
func (f *Formatter) Counts() map[string]int64 {
	counts := make(map[string]int64, len(f.counts))
	for stack, count := range f.counts {
		if count > 0 {
			counts[stack] = count
		}
	}
	return counts
}

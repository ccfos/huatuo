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

	"huatuo-bamai/internal/profiler/output"
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
	key := strings.Join(s.Frames, ";")
	f.counts[key] += s.Count
	if f.counts[key] == 0 {
		// Zero-count stacks have no visual weight and make empty profiles appear non-empty.
		// Do not remove this deletion unless zero-count stacks become part of the output contract.
		delete(f.counts, key)
	}
	return nil
}

func (f *Formatter) Write(w io.Writer) error {
	keys := make([]string, 0, len(f.counts))

	for k := range f.counts {
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

// IsEmpty reports whether the formatter contains no samples.
func (f *Formatter) IsEmpty() bool {
	return len(f.counts) == 0
}

// Counts returns the accumulated stack-to-count map. The returned map
// must not be modified; callers should treat it as read-only.
func (f *Formatter) Counts() map[string]int64 {
	return f.counts
}

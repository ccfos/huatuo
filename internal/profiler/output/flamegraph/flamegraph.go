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

// Package flamegraph writes an interactive SVG flame graph via the JS-enhanced renderer.
package flamegraph

import (
	"html"
	"io"
	"sort"
	"strings"

	"huatuo-bamai/internal/profiler/output"
)

// Formatter accumulates samples and renders an SVG flame graph.
type Formatter struct {
	counts map[string]int64
	style  Style
}

var _ output.Formatter = (*Formatter)(nil)

// New returns a Formatter with the default SVG style.
func New() *Formatter {
	return &Formatter{
		counts: make(map[string]int64),
		style:  DefaultStyle,
	}
}

// NewWithStyle returns a Formatter with a custom SVG style.
func NewWithStyle(style Style) *Formatter {
	return &Formatter{
		counts: make(map[string]int64),
		style:  style,
	}
}

func (f *Formatter) Name() string { return "flamegraph" }

func (f *Formatter) Add(s *output.Sample) error {
	if len(s.Frames) == 0 {
		return nil
	}
	key := strings.Join(s.Frames, ";")
	f.counts[key] += s.Count
	return nil
}

func (f *Formatter) Write(w io.Writer) error {
	return RenderStyle(f.toStacks(), w, f.style)
}

func (f *Formatter) Reset() {
	f.counts = make(map[string]int64)
}

// toStacks converts the count map to []Stack, HTML-escaping frame names.
func (f *Formatter) toStacks() []Stack {
	stacks := make([]Stack, 0, len(f.counts))

	for stackStr, count := range f.counts {
		stackStr = strings.ReplaceAll(stackStr, "\"", "")
		parts := strings.Split(stackStr, ";")
		names := make([]string, 0, len(parts))

		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				names = append(names, html.EscapeString(p))
			}
		}
		if len(names) > 0 {
			stacks = append(stacks, Stack{
				Names:   names,
				Samples: count,
			})
		}
	}

	sort.Slice(stacks, func(i, j int) bool {
		return stacks[i].Samples > stacks[j].Samples
	})
	return stacks
}

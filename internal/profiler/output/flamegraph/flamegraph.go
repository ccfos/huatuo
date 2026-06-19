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
	"io"
	"sort"
	"strings"

	"huatuo-bamai/internal/profiler/output"
	"huatuo-bamai/internal/profiler/output/raw"
)

// Formatter accumulates samples and renders an SVG flame graph.
type Formatter struct {
	*raw.Formatter
	style Style
}

var _ output.Formatter = (*Formatter)(nil)

func init() {
	output.RegisterFormatter(output.FormatFlameGraph, func() output.Formatter { return New() })
	output.RegisterFormatter(output.FormatSVG, func() output.Formatter { return New() })
}

// New returns a Formatter with the default SVG style.
func New() *Formatter {
	return &Formatter{
		Formatter: raw.New(),
		style:     DefaultStyle,
	}
}

func (f *Formatter) Name() string { return "flamegraph" }

func (f *Formatter) Write(w io.Writer) error {
	return RenderStyle(f.toStacks(), w, f.style)
}

// toStacks converts the count map to []Stack.
func (f *Formatter) toStacks() []Stack {
	counts := f.Counts()
	stacks := make([]Stack, 0, len(counts))

	for stackStr, count := range counts {
		parts := strings.Split(stackStr, ";")
		names := make([]string, 0, len(parts))

		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				names = append(names, p)
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

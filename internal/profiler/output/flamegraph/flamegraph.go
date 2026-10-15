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
	"context"
	"io"
	"sort"
	"strings"

	"github.com/ccfos/huatuo/internal/profiler/output"
	"github.com/ccfos/huatuo/internal/profiler/output/raw"
	"github.com/ccfos/huatuo/pkg/types"
)

// formatter accumulates windows and writes an SVG flame graph at completion.
type formatter struct {
	stacks      raw.Stacks
	destination *output.LocalDestination
	style       Style
}

var _ output.Formatter = (*formatter)(nil)

// NewFormatter initializes SVG output.
func NewFormatter(cfg output.FormatterConfig) (output.Formatter, error) {
	cfg.Format = output.FormatSVG
	destination, err := output.NewLocalDestination(cfg)
	if err != nil {
		return nil, err
	}

	return &formatter{destination: destination, style: DefaultStyle}, nil
}

func (f *formatter) Add(ctx context.Context, window *types.ProfilingWindow) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return output.WalkProfile(ctx, window.Profile, f.stacks.Add)
}

func (f *formatter) Complete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if f.stacks.IsEmpty() && !f.destination.IsStdout() {
		return nil
	}

	return f.destination.WriteAndClose(func(w io.Writer) error {
		return RenderStyle(f.toStacks(), w, f.style)
	})
}

func (f *formatter) Close() error { return f.destination.Close() }

// toStacks converts the count map to []Stack.
func (f *formatter) toStacks() []Stack {
	counts := f.stacks.Counts()
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

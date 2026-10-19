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
	"fmt"
	"io"
	"math"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/types"
)

// formatter accumulates windows and writes an SVG flame graph at completion.
type formatter struct {
	processor   processor
	destination *output.LocalDestination
	style       Style
	total       int64
}

var _ output.Formatter = (*formatter)(nil)

// NewFormatter initializes SVG output.
func NewFormatter(cfg output.Config) (output.Formatter, error) {
	cfg.Format = output.FormatSVG
	destination, err := output.NewLocalDestination(cfg)
	if err != nil {
		return nil, err
	}

	return &formatter{
		destination: destination,
		style:       DefaultStyle,
	}, nil
}

func (f *formatter) Add(ctx context.Context, window *types.AggregationWindow) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return output.WalkWindow(ctx, window, func(frames []string, value int64) error {
		if value < 0 {
			return fmt.Errorf("flamegraph: negative sample weight %d", value)
		}

		if value > math.MaxInt64-f.total {
			return fmt.Errorf("flamegraph: total weight overflows int64")
		}

		f.total += value
		f.processor.Process(Stack{
			Names:   frames,
			Samples: value,
		})

		return nil
	})
}

func (f *formatter) Complete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	f.processor.prune()
	if len(f.processor.frames) == 0 && !f.destination.IsStdout() {
		return nil
	}

	return f.destination.WriteAndClose(func(w io.Writer) error {
		return f.processor.render(w, f.style)
	})
}

func (f *formatter) Close() error { return f.destination.Close() }

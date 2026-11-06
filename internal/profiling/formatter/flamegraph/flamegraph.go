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

	"github.com/ccfos/huatuo/internal/profiling/aggregation"
	profilewindow "github.com/ccfos/huatuo/internal/profiling/window"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/types"
)

// formatter accumulates windows and writes an SVG flame graph at completion.
type formatter struct {
	aggregator  *aggregation.Aggregator
	taskID      string
	destination *output.LocalDestination
	style       Style
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
		aggregator:  aggregation.New(),
		taskID:      cfg.TaskID,
		style:       DefaultStyle,
	}, nil
}

func (*formatter) Name() output.OutputFormat { return output.FormatSVG }

func (f *formatter) Add(ctx context.Context, w *types.AggregationWindow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	metadata, err := profilewindow.Metadata(w.ProfileType)
	if err != nil {
		return err
	}
	f.style.ValueUnit = metadata[2]

	var frames []types.ProfileFrame
	a := &w.Aggregated
	for i := range a.Samples {
		if err := ctx.Err(); err != nil {
			return err
		}

		sample := &a.Samples[i]
		var kernelStart int
		frames, kernelStart = profilewindow.AppendFrames(frames[:0], a, sample)
		if err := f.aggregator.Add(&aggregation.Sample{
			UserFrames:   frames[:kernelStart],
			KernelFrames: frames[kernelStart:],
			Labels:       profilewindow.Labels(a, sample),
			Value:        sample.Value,
		}); err != nil {
			return err
		}
	}

	return nil
}

func (f *formatter) Complete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if f.aggregator.Empty() && !f.destination.IsStdout() {
		return nil
	}

	w := types.AggregationWindow{Aggregated: f.aggregator.Drain()}
	if err := output.ClampNegativeSamples(ctx, f.taskID, &w.Aggregated); err != nil {
		return err
	}
	var tree processor
	var total int64
	if err := output.WalkWindow(ctx, &w, func(frames []string, value int64) error {
		// Zero identities remain in the aggregate but must not affect graph height.
		if value == 0 {
			return nil
		}

		if value > math.MaxInt64-total {
			return fmt.Errorf("flamegraph: total weight overflows int64")
		}

		total += value
		tree.Process(Stack{
			Names:   frames,
			Samples: value,
		})
		return nil
	}); err != nil {
		return err
	}

	return f.destination.WriteAndClose(func(w io.Writer) error {
		return tree.render(w, f.style)
	})
}

func (f *formatter) Close() error { return f.destination.Close() }

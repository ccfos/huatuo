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

package flamegraph

import (
	"context"
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"
	flamegraphtui "github.com/ccfos/huatuo/internal/profiling/flamegraph/tui"
	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	profilewindow "github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/types"
)

// tuiFormatter keeps signed samples until the final window has been processed.
type tuiFormatter struct {
	aggregator *aggregation.Aggregator
	taskID     string
	model      *flamegraphtui.Model
}

var _ output.Formatter = (*tuiFormatter)(nil)

// NewTUIFormatter validates the terminal before collection starts.
func NewTUIFormatter(cfg output.Config) (output.Formatter, error) {
	if cfg.Destination != "" && cfg.Destination != "stdout" && cfg.Destination != "-" {
		return nil, fmt.Errorf("tui output requires --output-path to be omitted or set to stdout or -")
	}
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		return nil, fmt.Errorf("tui output requires terminal stdin and stdout")
	}
	return &tuiFormatter{
		aggregator: aggregation.New(),
		taskID:     cfg.TaskID,
	}, nil
}

func (*tuiFormatter) Name() output.OutputFormat { return output.FormatTUI }

func (f *tuiFormatter) Add(ctx context.Context, w *types.AggregationWindow) error {
	if err := ctx.Err(); err != nil {
		return err
	}

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

func (f *tuiFormatter) Complete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if f.aggregator.Empty() {
		return nil
	}

	w := types.AggregationWindow{Aggregated: f.aggregator.Drain()}
	if err := output.ClampNegativeSamples(ctx, f.taskID, &w.Aggregated); err != nil {
		return err
	}
	stacks := flamegraphtui.NewStackTree(ctx)
	if err := output.WalkWindow(ctx, &w, stacks.Add); err != nil {
		return err
	}

	model, err := stacks.BuildModel()
	if err != nil {
		return err
	}
	f.model = model
	return nil
}

func (f *tuiFormatter) View(ctx context.Context) error {
	if f.model == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	_, err := tea.NewProgram(f.model, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) && errors.Is(err, tea.ErrProgramKilled) {
		return ctx.Err()
	}
	return err
}

func (f *tuiFormatter) Close() error {
	f.aggregator.Drain()
	f.model = nil
	return nil
}

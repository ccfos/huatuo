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
	"io"
	"slices"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"
	flamegraphtui "github.com/ccfos/huatuo/internal/profiling/flamegraph/tui"
	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	profilewindow "github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/types"
)

// tuiFormatter keeps signed samples until the final window has been processed.
type tuiFormatter struct {
	aggregator  *aggregation.Aggregator
	taskID      string
	profileType string
	destination *output.LocalDestination // nil for terminal-only output
	report      *tuiReport
	model       *flamegraphtui.Model
}

var _ output.Formatter = (*tuiFormatter)(nil)

// NewTUIFormatter validates the terminal before collection starts.
func NewTUIFormatter(cfg output.Config) (output.Formatter, error) {
	if err := output.ValidateTUITerminal(); err != nil {
		return nil, err
	}

	f := &tuiFormatter{
		aggregator: aggregation.New(),
		taskID:     cfg.TaskID,
	}
	if cfg.Destination != "" {
		cfg.Format = output.FormatTUI
		destination, err := output.NewLocalDestination(cfg)
		if err != nil {
			return nil, err
		}
		if !destination.IsStdout() {
			f.destination = destination
		}
	}
	return f, nil
}

func (*tuiFormatter) Name() output.OutputFormat { return output.FormatTUI }

func (f *tuiFormatter) Add(ctx context.Context, w *types.AggregationWindow) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	f.profileType = w.ProfileType
	return f.aggregator.AddProfile(ctx, &w.Aggregated)
}

func (f *tuiFormatter) Complete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if f.aggregator.Empty() {
		return nil
	}

	aggregated := f.aggregator.Drain()
	if err := output.ClampNegativeSamples(ctx, f.taskID, &aggregated); err != nil {
		return err
	}
	var unit string
	if f.profileType != "" {
		metadata, err := profilewindow.Metadata(f.profileType)
		if err != nil {
			return err
		}
		unit = metadata[2]
	}

	model, err := prepareViewer(ctx, &aggregated, unit)
	if err != nil {
		return err
	}
	f.model = model
	if model != nil && f.destination != nil {
		f.report = &tuiReport{
			Version:     tuiReportVersion,
			ProfileType: f.profileType,
			Aggregated:  aggregated,
		}
	}
	return nil
}

func prepareViewer(ctx context.Context, aggregated *types.AggregatedProfile, unit string) (*flamegraphtui.Model, error) {
	table := make(flamegraphtui.TopTable)
	tree, err := buildProfileTree(ctx, aggregated, nil, table)
	if err != nil {
		return nil, err
	}

	// The drained or decoded samples remain immutable while the viewer uses them.
	return tree.BuildProfileModel(table, unit, func(ctx context.Context, tags []string) (*flamegraphtui.StackTree, error) {
		return buildProfileTree(ctx, aggregated, tags, nil)
	})
}

func buildProfileTree(
	ctx context.Context,
	aggregated *types.AggregatedProfile,
	tags []string,
	table flamegraphtui.TopTable,
) (*flamegraphtui.StackTree, error) {
	tree := flamegraphtui.NewStackTree(ctx)
	var path []string
	err := output.WalkSamples(ctx, aggregated, func(frames []string, labels []types.ProfileLabel, weight int64) error {
		if weight == 0 {
			return nil
		}

		stack := frames
		if len(tags) > 0 {
			path = append(appendGroupLabels(path[:0], labels, tags), frames...)
			stack = path
		}
		if err := tree.Add(stack, weight); err != nil {
			return err
		}
		if table == nil {
			return nil
		}

		return table.Add(ctx, frames, weight)
	})
	if err != nil {
		return nil, err
	}

	return tree, nil
}

func appendGroupLabels(path []string, labels []types.ProfileLabel, tags []string) []string {
	for _, tag := range tags {
		index := slices.IndexFunc(labels, func(label types.ProfileLabel) bool {
			return label.Name == tag
		})
		switch {
		case index < 0:
			// Keep one level per tag, even for samples without that label.
			path = append(path, tag+"=?")
		case tag == "cpu":
			path = append(path, tag+labels[index].StringValue)
		default:
			path = output.DisplayLabels(path, labels[index:index+1])
		}
	}
	return path
}

func (f *tuiFormatter) View(ctx context.Context) error {
	if f.model == nil {
		return nil
	}
	quit, err := flamegraphtui.Run(ctx, f.model)
	if err != nil {
		return err
	}
	if !quit || f.report == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return f.destination.WriteAndClose(func(w io.Writer) error {
		return writeTUIReport(w, f.report)
	})
}

func (f *tuiFormatter) Close() error {
	f.aggregator.Drain()
	f.model = nil
	f.report = nil
	return nil
}

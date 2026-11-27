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

// Package tui formats profiling windows for terminal viewing and gob replay.
package tui

import (
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"slices"
	"strings"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"
	flamegraphtui "github.com/ccfos/huatuo/internal/profiling/flamegraph/tui"
	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	profilewindow "github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

// tuiFormatter keeps signed samples until the final window has been processed.
type tuiFormatter struct {
	aggregator  *aggregation.Aggregator
	taskID      string
	destination *output.LocalDestination // nil for terminal-only output
	profile     tuiProfile
	model       *flamegraphtui.Model
}

var _ output.Formatter = (*tuiFormatter)(nil)

// NewFormatter validates the terminal before collection starts.
func NewFormatter(cfg output.Config) (output.Formatter, error) {
	if err := output.ValidateTUITerminal(); err != nil {
		return nil, err
	}

	f := &tuiFormatter{
		aggregator: aggregation.New(),
		taskID:     cfg.TaskID,
	}
	if cfg.Destination != "" {
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

	f.profile.ProfileType = w.ProfileType
	for reason, count := range w.DroppedSamples {
		if f.profile.DroppedSamples == nil {
			f.profile.DroppedSamples = make(map[string]uint64)
		}
		if count > math.MaxUint64-f.profile.DroppedSamples[reason] {
			return fmt.Errorf("dropped sample count for %q exceeds uint64", reason)
		}
		f.profile.DroppedSamples[reason] += count
	}
	return f.aggregator.AddProfile(ctx, &w.Aggregated)
}

func (f *tuiFormatter) Complete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	aggregated := f.aggregator.Drain()
	f.aggregator = nil

	if err := output.ClampNegativeSamples(ctx, f.taskID, &aggregated); err != nil {
		return err
	}
	var unit string
	if f.profile.ProfileType != "" {
		metadata, err := profilewindow.Metadata(f.profile.ProfileType)
		if err != nil {
			return err
		}
		unit = metadata[2]
	}

	f.profile.Version = tuiGobVersion
	f.profile.Aggregated = aggregated
	model, err := buildViewerModel(ctx, &f.profile, unit)
	if err != nil {
		return err
	}
	f.model = model
	if model == nil {
		fmt.Fprintln(os.Stderr, "No displayable profiling samples. No new report was saved.")
		return nil
	}
	if f.destination != nil {
		// A canceled save may still encode the immutable samples after Close.
		profile := f.profile
		path, err := f.destination.SaveAtomically(ctx, ".gob", func(w io.Writer) error {
			return writeTUIGob(ctx, w, &profile)
		})
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Saved profiling report to %s\n", path)
	}
	return nil
}

func buildViewerModel(ctx context.Context, profile *tuiProfile, unit string) (*flamegraphtui.Model, error) {
	aggregated := &profile.Aggregated
	tree, err := buildProfileTree(ctx, aggregated, nil)
	if err != nil {
		return nil, err
	}

	// The drained or decoded samples remain immutable while the viewer uses them.
	labelNames := []string{"cpu", "comm", "pid"}
	if profile.ProfileType == profiling.ProfileTypeIRQTracingSample {
		labelNames = append(labelNames, "irq_context", "irq_type", "irq_vector")
	}
	var summary string
	if profile.Version == 1 {
		summary = " | completeness unknown"
	} else if len(profile.DroppedSamples) > 0 {
		var counts []string
		for _, reason := range []string{types.DropReasonCollection, types.DropReasonAggregationQueue} {
			if count := profile.DroppedSamples[reason]; count > 0 {
				counts = append(counts, fmt.Sprintf("%s=%d", reason, count))
			}
		}
		if len(counts) > 0 {
			summary = " | incomplete: " + strings.Join(counts, ", ")
		}
	}
	return tree.BuildProfileModel(unit, summary, labelNames, func(ctx context.Context, groupLabelNames []string) (*flamegraphtui.StackTree, error) {
		return buildProfileTree(ctx, aggregated, groupLabelNames)
	})
}

func buildProfileTree(
	ctx context.Context,
	aggregated *types.AggregatedProfile,
	groupLabelNames []string,
) (*flamegraphtui.StackTree, error) {
	tree := flamegraphtui.NewStackTree(ctx)
	var path []string
	err := output.WalkSamples(ctx, aggregated, func(frames []string, labels []types.ProfileLabel, weight int64) error {
		if weight == 0 {
			return nil
		}

		stack := frames
		if len(groupLabelNames) > 0 {
			path = append(appendGroupLabels(path[:0], labels, groupLabelNames), frames...)
			stack = path
		}
		return tree.Add(stack, weight)
	})
	if err != nil {
		return nil, err
	}

	return tree, nil
}

func appendGroupLabels(path []string, labels []types.ProfileLabel, groupLabelNames []string) []string {
	for _, groupLabel := range groupLabelNames {
		index := slices.IndexFunc(labels, func(label types.ProfileLabel) bool {
			return label.Name == groupLabel
		})
		switch {
		case index < 0:
			// Keep one level per groupLabel, even for samples without that label.
			path = append(path, groupLabel+"=?")
		case groupLabel == "cpu":
			path = append(path, groupLabel+labels[index].StringValue)
		default:
			path = append(path, output.DisplayLabel(&labels[index], labels))
		}
	}
	return path
}

func (f *tuiFormatter) View(ctx context.Context) error {
	if f.model == nil {
		return nil
	}
	return flamegraphtui.Run(ctx, f.model, flamegraphtui.CallerManagedSignals)
}

func (f *tuiFormatter) Close() error {
	f.aggregator = nil
	f.model = nil
	f.profile = tuiProfile{}
	return nil
}

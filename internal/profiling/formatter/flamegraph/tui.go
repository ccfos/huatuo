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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"

	"github.com/charmbracelet/x/term"

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
	if err := validateTerminal(); err != nil {
		return nil, err
	}

	f := &tuiFormatter{
		aggregator: aggregation.New(),
		taskID:     cfg.TaskID,
	}
	if cfg.Destination != "" {
		cfg.Format = output.FormatJSON
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

func validateTerminal() error {
	if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
		return fmt.Errorf("tui output requires terminal stdin and stdout")
	}
	return nil
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
	defaultTree := flamegraphtui.NewStackTree(ctx)
	var mergedCPUTree *flamegraphtui.StackTree
	// The merged tree also needs samples without a CPU label, so start it before walking samples.
	for _, labels := range aggregated.LabelSets {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if findCPULabelIndex(labels) >= 0 {
			mergedCPUTree = flamegraphtui.NewStackTree(ctx)
			break
		}
	}

	table := make(flamegraphtui.TopTable)
	var path []string
	var hasCPUSamples bool
	err := output.WalkSamples(ctx, aggregated, func(frames []string, labels []types.ProfileLabel, weight int64) error {
		if weight == 0 {
			return nil
		}
		path = append(output.DisplayLabels(path[:0], labels), frames...)
		if err := defaultTree.Add(path, weight); err != nil {
			return err
		}
		if mergedCPUTree != nil {
			if index := findCPULabelIndex(labels); index >= 0 {
				// Trees consume the path synchronously, so reuse its storage.
				path = slices.Delete(path, index, index+1)
				hasCPUSamples = true
			}
			if err := mergedCPUTree.Add(path, weight); err != nil {
				return err
			}
		}
		return table.Add(ctx, frames, weight)
	})
	if err != nil {
		return nil, err
	}
	if !hasCPUSamples {
		mergedCPUTree = nil
	}
	return defaultTree.BuildProfileModel(mergedCPUTree, table, unit)
}

// findCPULabelIndex returns -1 when the labels do not include a CPU label.
func findCPULabelIndex(labels []types.ProfileLabel) int {
	return slices.IndexFunc(labels, func(label types.ProfileLabel) bool {
		return label.Name == "cpu"
	})
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
	if err := f.report.validateUTF8(ctx); err != nil {
		return fmt.Errorf("save report: %w", err)
	}
	return f.destination.WriteAndClose(func(w io.Writer) error {
		return json.NewEncoder(w).Encode(f.report)
	})
}

func (f *tuiFormatter) Close() error {
	f.aggregator.Drain()
	f.model = nil
	f.report = nil
	return nil
}

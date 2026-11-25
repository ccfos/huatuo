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

package collapsed

import (
	"context"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"
	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/types"
)

// formatter accumulates windows and writes folded stack weights at completion.
type formatter struct {
	aggregator  *aggregation.Aggregator
	taskID      string
	destination *output.LocalDestination
}

var _ output.Formatter = (*formatter)(nil)

// NewFormatter initializes collapsed output.
func NewFormatter(cfg output.Config) (output.Formatter, error) {
	cfg.Format = output.FormatCollapsed
	destination, err := output.NewLocalDestination(cfg)
	if err != nil {
		return nil, err
	}

	return &formatter{
		destination: destination,
		aggregator:  aggregation.New(),
		taskID:      cfg.TaskID,
	}, nil
}

func (*formatter) Name() output.OutputFormat { return output.FormatCollapsed }

func (f *formatter) Add(ctx context.Context, w *types.AggregationWindow) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return f.aggregator.AddProfile(ctx, &w.Aggregated)
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
	var stacks Stacks
	if err := output.WalkWindow(ctx, &w, stacks.Add); err != nil {
		return err
	}

	return f.destination.WriteAndClose(stacks.Write)
}

func (f *formatter) Close() error { return f.destination.Close() }

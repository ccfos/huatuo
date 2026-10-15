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

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/types"
)

// formatter accumulates windows and writes folded stack weights at completion.
type formatter struct {
	stacks      Stacks
	destination *output.LocalDestination
}

var _ output.Formatter = (*formatter)(nil)

// NewFormatter initializes collapsed output.
func NewFormatter(cfg output.FormatterConfig) (output.Formatter, error) {
	cfg.Format = output.FormatCollapsed
	destination, err := output.NewLocalDestination(cfg)
	if err != nil {
		return nil, err
	}

	return &formatter{destination: destination}, nil
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

	return f.destination.WriteAndClose(f.stacks.Write)
}

func (f *formatter) Close() error { return f.destination.Close() }

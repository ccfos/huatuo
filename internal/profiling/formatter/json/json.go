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

package json

import (
	"context"
	stdjson "encoding/json"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	profilewindow "github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/types"
)

type formatter struct {
	destination *output.LocalDestination
}

func (*formatter) Name() output.OutputFormat { return output.FormatJSON }

func (f *formatter) Add(ctx context.Context, window *types.AggregationWindow) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := output.RequireWindow(window); err != nil {
		return err
	}

	writer, err := f.destination.Open()
	if err != nil {
		return err
	}

	p, err := profilewindow.BuildProfile(ctx, window)
	if err != nil {
		return err
	}

	return stdjson.NewEncoder(writer).Encode(&types.ProfilingWindow{
		ContainerID:    window.ContainerID,
		ProfileType:    window.ProfileType,
		Profile:        p,
		DroppedSamples: window.DroppedSamples,
	})
}

func (f *formatter) Complete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	return f.destination.Close()
}

// NewFormatter initializes window JSON output.
func NewFormatter(cfg output.Config) (output.Formatter, error) {
	cfg.Format = output.FormatJSON
	destination, err := output.NewLocalDestination(cfg)
	if err != nil {
		return nil, err
	}

	return &formatter{destination: destination}, nil
}

func (f *formatter) Close() error { return f.destination.Close() }

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

// Package speedscope encodes aggregated profiles as weighted Speedscope samples.
package speedscope

import (
	"context"
	"fmt"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"
	profilewindow "github.com/ccfos/huatuo/internal/profiling/window"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/types"
)

// formatter owns accumulated samples and never retains caller profile buffers.
// Samples describe aggregate weights, not the chronological order of observations.
type formatter struct {
	taskID       string
	pending      []pendingProfile
	pendingIndex map[[2]string]int
	destination  *output.LocalDestination
	sampleType   string
	sampleUnit   string
}

// Keep container/profile grouping until presentation; window dictionary IDs
// and display frame names cannot identify samples across windows.
type pendingProfile struct {
	containerID string
	profileType string
	aggregator  *aggregation.Aggregator
}

var _ output.Formatter = (*formatter)(nil)

// NewFormatter initializes speedscope output.
func NewFormatter(cfg output.Config) (output.Formatter, error) {
	cfg.Format = output.FormatSpeedscope
	destination, err := output.NewLocalDestination(cfg)
	if err != nil {
		return nil, err
	}

	return &formatter{
		destination:  destination,
		taskID:       cfg.TaskID,
		pendingIndex: make(map[[2]string]int),
	}, nil
}

func (*formatter) Name() output.OutputFormat { return output.FormatSpeedscope }

func (f *formatter) Complete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	windows := make([]types.AggregationWindow, 0, len(f.pending))
	profiles := make([]*types.AggregatedProfile, 0, len(f.pending))
	for i := range f.pending {
		pending := &f.pending[i]
		if pending.aggregator.Empty() {
			continue
		}

		w := types.AggregationWindow{
			ContainerID: pending.containerID,
			ProfileType: pending.profileType,
			Aggregated:  pending.aggregator.Drain(),
		}
		windows = append(windows, w)
		profiles = append(profiles, &windows[len(windows)-1].Aggregated)
	}

	output.ClampNegativeSamples(f.taskID, profiles...)
	result := encoder{
		unit:      f.sampleUnit,
		frames:    make(map[frame]int),
		frameList: []frame{},
	}
	for i := range windows {
		if err := result.appendProfiles(ctx, &windows[i]); err != nil {
			return err
		}
	}

	if len(result.profiles) == 0 {
		return nil
	}

	return f.destination.WriteAndClose(result.write)
}

func (f *formatter) Close() error { return f.destination.Close() }

func (f *formatter) Add(ctx context.Context, w *types.AggregationWindow) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	parts, err := profilewindow.Metadata(w.ProfileType)
	if err != nil {
		return err
	}

	sampleType, sampleUnit := parts[1], parts[2]
	switch sampleUnit {
	case "count", "counts", "nanoseconds", "microseconds", "milliseconds", "seconds", "bytes", "none":
	default:
		return fmt.Errorf("speedscope: unsupported sample unit %q", sampleUnit)
	}

	if f.sampleType != "" && (f.sampleType != sampleType || f.sampleUnit != sampleUnit) {
		return fmt.Errorf("speedscope: cannot mix sample types or units")
	}

	f.sampleType, f.sampleUnit = sampleType, sampleUnit
	key := [2]string{w.ContainerID, w.ProfileType}
	index, ok := f.pendingIndex[key]
	if !ok {
		index = len(f.pending)
		f.pendingIndex[key] = index
		f.pending = append(f.pending, pendingProfile{
			containerID: w.ContainerID,
			profileType: w.ProfileType,
			aggregator:  aggregation.New(),
		})
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
		if err := f.pending[index].aggregator.Add(&aggregation.Sample{
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

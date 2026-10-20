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
	"encoding/json"
	"fmt"
	"io"

	profilewindow "github.com/ccfos/huatuo/internal/profiling/window"

	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/types"
)

type speedscopeFile struct {
	Schema           string           `json:"$schema"`
	Shared           sharedData       `json:"shared"`
	Profiles         []sampledProfile `json:"profiles"`
	ActiveProfileIdx int              `json:"activeProfileIndex"`
	Exporter         string           `json:"exporter"`
	Name             string           `json:"name"`
}

type sharedData struct {
	Frames []frame `json:"frames"`
}

type frame struct {
	Name string `json:"name"`
	File string `json:"file,omitempty"`
	Line int64  `json:"line,omitempty"`
}

type sampledProfile struct {
	Type       string  `json:"type"`
	Name       string  `json:"name"`
	Unit       string  `json:"unit"`
	StartValue int64   `json:"startValue"`
	EndValue   int64   `json:"endValue"`
	Samples    [][]int `json:"samples"`
	Weights    []int64 `json:"weights"`
}

// formatter retains owned frame indices and weights, never caller profile buffers.
// Samples describe aggregate weights, not the chronological order of observations.
type formatter struct {
	destination *output.LocalDestination
	frames      map[frame]int
	frameList   []frame
	profiles    []sampledProfile
	groups      map[string]int
	sampleType  string
	sampleUnit  string
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
		destination: destination,
		frames:      make(map[frame]int),
		groups:      make(map[string]int),
	}, nil
}

func (f *formatter) Complete(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	if len(f.profiles) == 0 {
		return nil
	}

	active := 0
	for i := range f.profiles {
		if f.profiles[i].EndValue > f.profiles[active].EndValue {
			active = i
		}
	}

	return f.destination.WriteAndClose(func(w io.Writer) error {
		return json.NewEncoder(w).Encode(speedscopeFile{
			Schema:           "https://www.speedscope.app/file-format-schema.json",
			Shared:           sharedData{Frames: f.frameList},
			Profiles:         f.profiles,
			ActiveProfileIdx: active,
			Exporter:         "huatuo-profiler",
			Name:             "aggregated profile",
		})
	})
}

func (f *formatter) Close() error { return f.destination.Close() }

// Browser viewers use IEEE-754 numbers for both weights and source lines.
const maxExactInteger int64 = 1<<53 - 1

func (f *formatter) Add(ctx context.Context, w *types.AggregationWindow) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	parts, err := profilewindow.Metadata(w.ProfileType)
	if err != nil {
		return err
	}

	sampleType, sampleUnit := parts[1], parts[2]
	unit := sampleUnit
	switch unit {
	case "count", "counts":
		unit = "none"
	case "nanoseconds", "microseconds", "milliseconds", "seconds", "bytes", "none":
	default:
		return fmt.Errorf("speedscope: unsupported sample unit %q", unit)
	}

	if f.sampleType != "" && (f.sampleType != sampleType || f.sampleUnit != sampleUnit) {
		return fmt.Errorf("speedscope: cannot mix sample types or units")
	}

	f.sampleType, f.sampleUnit = sampleType, sampleUnit
	a := &w.Aggregated
	var sourceFrames []types.ProfileFrame
	for i := range a.Samples {
		if err := ctx.Err(); err != nil {
			return err
		}

		sample := &a.Samples[i]
		weight := sample.Value
		if weight < 0 || weight > maxExactInteger {
			return fmt.Errorf("speedscope: weight %d is outside [0, 2^53-1]", weight)
		}

		if weight == 0 {
			continue
		}

		labels := profilewindow.Labels(a, sample)
		keyBytes, err := json.Marshal(struct {
			Container string               `json:"container"`
			Profile   string               `json:"profile"`
			Labels    []types.ProfileLabel `json:"labels"`
		}{
			Container: w.ContainerID,
			Profile:   w.ProfileType,
			Labels:    labels,
		})
		if err != nil {
			return err
		}

		key := string(keyBytes)
		index, exists := f.groups[key]
		if !exists {
			index = len(f.profiles)
			f.groups[key] = index
			f.profiles = append(f.profiles, sampledProfile{
				Type: "sampled",
				Name: key,
				Unit: unit,
			})
		}

		target := &f.profiles[index]
		if weight > maxExactInteger-target.EndValue {
			return fmt.Errorf("speedscope: total weight exceeds 2^53-1")
		}

		var kernelStart int
		sourceFrames, kernelStart = profilewindow.AppendFrames(sourceFrames[:0], a, sample)
		if len(sourceFrames) == 0 {
			sourceFrames = append(sourceFrames, types.ProfileFrame{Name: "[unknown]"})
			kernelStart = 1
		}

		indices := make([]int, len(sourceFrames))
		for j := range sourceFrames {
			source := &sourceFrames[j]
			if source.Line < 0 || source.Line > maxExactInteger {
				return fmt.Errorf("speedscope: invalid source line %d", source.Line)
			}

			item := frame{
				Name: source.Name,
				File: source.File,
				Line: source.Line,
			}
			if j >= kernelStart {
				item.Name += "_[k]"
			}

			id, ok := f.frames[item]
			if !ok {
				id = len(f.frameList)
				f.frames[item] = id
				f.frameList = append(f.frameList, item)
			}

			indices[j] = id
		}

		target.Samples = append(target.Samples, indices)
		target.Weights = append(target.Weights, weight)
		target.EndValue += weight
	}

	return nil
}

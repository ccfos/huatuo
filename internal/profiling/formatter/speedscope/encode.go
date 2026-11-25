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

package speedscope

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"

	profilewindow "github.com/ccfos/huatuo/internal/profiling/window"
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

// Display suffixes are not identities: a user symbol may already end in _[k].
type frameKey struct {
	frame
	isKernel bool
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

// encoder owns presentation data only for the duration of Complete.
type encoder struct {
	unit      string
	frames    map[frameKey]int
	frameList []frame
	profiles  []sampledProfile
}

// Browser viewers use IEEE-754 numbers for both weights and source lines.
const maxExactInteger int64 = 1<<53 - 1

func (e *encoder) appendProfiles(ctx context.Context, w *types.AggregationWindow) error {
	unit := e.unit
	if unit == "count" || unit == "counts" {
		unit = "none"
	}

	// Each call consumes one container/profile aggregate; label IDs are local to it.
	a := &w.Aggregated
	groups := make([]struct {
		index       int // Output profile index plus one; zero means unseen.
		sampleCount int
	}, len(a.LabelSets)+1)
	var userFrameCount, kernelFrameCount int
	hasUnknown := false
	for i := range a.Samples {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		sample := &a.Samples[i]
		weight := sample.Value
		if weight < 0 || weight > maxExactInteger {
			return fmt.Errorf("speedscope: container %q profile %q label set %d: weight %d is outside [0, 2^53-1]",
				w.ContainerID, w.ProfileType, sample.LabelSet, weight)
		}
		if weight == 0 {
			continue
		}

		groups[sample.LabelSet].sampleCount++
		if sample.UserStack != 0 {
			userFrameCount = min(len(a.Frames), userFrameCount+len(a.Stacks[sample.UserStack-1]))
		}
		if sample.KernelStack != 0 {
			kernelFrameCount = min(len(a.Frames), kernelFrameCount+len(a.Stacks[sample.KernelStack-1]))
		}
		hasUnknown = hasUnknown || (sample.UserStack == 0 && sample.KernelStack == 0)
	}

	frameCapacity := userFrameCount + kernelFrameCount
	if hasUnknown {
		frameCapacity++
	}
	if len(e.frames) == 0 {
		e.frames = make(map[frameKey]int, frameCapacity)
	}
	e.frameList = slices.Grow(e.frameList, frameCapacity)
	e.profiles = slices.Grow(e.profiles, min(len(groups), len(a.Samples)))

	// Source IDs are local to this aggregate; user and kernel frames have
	// separate mappings. Cached IDs are one-based so frame zero is cacheable.
	var frameIDs [2][]int
	if userFrameCount > 0 {
		frameIDs[0] = make([]int, len(a.Frames))
	}
	if kernelFrameCount > 0 {
		frameIDs[1] = make([]int, len(a.Frames))
	}

	for i := range a.Samples {
		if err := ctx.Err(); err != nil {
			return err
		}

		sample := &a.Samples[i]
		weight := sample.Value

		group := &groups[sample.LabelSet]
		if group.index == 0 {
			name, err := json.Marshal(struct {
				Container string               `json:"container"`
				Profile   string               `json:"profile"`
				Labels    []types.ProfileLabel `json:"labels"`
			}{
				Container: w.ContainerID,
				Profile:   w.ProfileType,
				Labels:    profilewindow.Labels(a, sample),
			})
			if err != nil {
				return err
			}

			group.index = len(e.profiles) + 1
			e.profiles = append(e.profiles, sampledProfile{
				Type:    "sampled",
				Name:    string(name),
				Unit:    unit,
				Samples: make([][]int, 0, group.sampleCount),
				Weights: make([]int64, 0, group.sampleCount),
			})
		}

		if weight == 0 {
			continue
		}

		target := &e.profiles[group.index-1]
		if weight > maxExactInteger-target.EndValue {
			return fmt.Errorf("speedscope: container %q profile %q label set %d: total weight exceeds 2^53-1",
				w.ContainerID, w.ProfileType, sample.LabelSet)
		}

		var stacks [2][]uint32
		if sample.UserStack != 0 {
			stacks[0] = a.Stacks[sample.UserStack-1]
		}
		if sample.KernelStack != 0 {
			stacks[1] = a.Stacks[sample.KernelStack-1]
		}

		indices := make([]int, len(stacks[0])+len(stacks[1]))
		if len(indices) == 0 {
			id, err := e.internFrame(frameKey{frame: frame{Name: "[unknown]"}})
			if err != nil {
				return err
			}
			indices = []int{id}
		}

		position := 0
		for domain, stack := range stacks {
			for _, sourceID := range stack {
				id := frameIDs[domain][sourceID]
				if id == 0 {
					source := &a.Frames[sourceID]
					index, err := e.internFrame(frameKey{
						frame: frame{
							Name: source.Name,
							File: source.File,
							Line: source.Line,
						},
						isKernel: domain == 1,
					})
					if err != nil {
						return fmt.Errorf("speedscope: container %q profile %q label set %d: %w",
							w.ContainerID, w.ProfileType, sample.LabelSet, err)
					}
					id = index + 1
					frameIDs[domain][sourceID] = id
				}
				indices[position] = id - 1
				position++
			}
		}

		target.Samples = append(target.Samples, indices)
		target.Weights = append(target.Weights, weight)
		target.EndValue += weight
	}

	return nil
}

func (e *encoder) internFrame(key frameKey) (int, error) {
	if key.Line < 0 || key.Line > maxExactInteger {
		return 0, fmt.Errorf("invalid source line %d for frame %q", key.Line, key.Name)
	}

	if id, ok := e.frames[key]; ok {
		return id, nil
	}

	id := len(e.frameList)
	e.frames[key] = id
	item := key.frame
	if key.isKernel {
		item.Name += "_[k]"
	}
	e.frameList = append(e.frameList, item)

	return id, nil
}

func (e *encoder) write(ctx context.Context, w io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	active := 0
	for i := range e.profiles {
		if e.profiles[i].EndValue > e.profiles[active].EndValue {
			active = i
		}
	}

	// Encode buffers the whole value before Write; check cancellation again
	// there so a deadline during encoding cannot publish a successful result.
	writer := writeFunc(func(data []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}

		n, err := w.Write(data)
		if err != nil {
			return n, err
		}

		return n, ctx.Err()
	})

	return json.NewEncoder(writer).Encode(speedscopeFile{
		Schema:           "https://www.speedscope.app/file-format-schema.json",
		Shared:           sharedData{Frames: e.frameList},
		Profiles:         e.profiles,
		ActiveProfileIdx: active,
		Exporter:         "huatuo-profiler",
		Name:             "aggregated profile",
	})
}

type writeFunc func([]byte) (int, error)

func (f writeFunc) Write(data []byte) (int, error) { return f(data) }

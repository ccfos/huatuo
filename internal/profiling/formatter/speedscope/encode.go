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
	frames    map[frame]int
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
	groups := make(map[uint32]int)
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

		index, exists := groups[sample.LabelSet]
		if !exists {
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

			index = len(e.profiles)
			groups[sample.LabelSet] = index
			e.profiles = append(e.profiles, sampledProfile{
				Type:    "sampled",
				Name:    string(name),
				Unit:    unit,
				Samples: [][]int{},
				Weights: []int64{},
			})
		}

		if weight == 0 {
			continue
		}

		target := &e.profiles[index]
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

			id, ok := e.frames[item]
			if !ok {
				id = len(e.frameList)
				e.frames[item] = id
				e.frameList = append(e.frameList, item)
			}

			indices[j] = id
		}

		target.Samples = append(target.Samples, indices)
		target.Weights = append(target.Weights, weight)
		target.EndValue += weight
	}

	return nil
}

func (e *encoder) write(w io.Writer) error {
	active := 0
	for i := range e.profiles {
		if e.profiles[i].EndValue > e.profiles[active].EndValue {
			active = i
		}
	}

	return json.NewEncoder(w).Encode(speedscopeFile{
		Schema:           "https://www.speedscope.app/file-format-schema.json",
		Shared:           sharedData{Frames: e.frameList},
		Profiles:         e.profiles,
		ActiveProfileIdx: active,
		Exporter:         "huatuo-profiler",
		Name:             "aggregated profile",
	})
}

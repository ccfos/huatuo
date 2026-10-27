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

package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	"google.golang.org/protobuf/encoding/protojson"

	mcpevent "github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/profiling/flamegraph"
)

const (
	ProfileRootLabel      = "[root]"
	MaxProfileSourceDepth = 1024
)

type profileNode struct {
	name     string
	value    int64
	self     int64
	children map[string]*profileNode
}

func normalizeProfile(ctx context.Context, source json.RawMessage) ([]flamegraph.FrameData, string, string, error) {
	var document struct {
		TracerData struct {
			FlameData json.RawMessage `json:"flamedata"`
		} `json:"tracer_data"`
	}
	if err := json.Unmarshal(source, &document); err != nil {
		return nil, "", "", err
	}
	raw := bytes.TrimSpace(document.TracerData.FlameData)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, "", "", fmt.Errorf("flamedata is empty")
	}
	if raw[0] == '[' {
		var frames []flamegraph.FrameData
		if err := json.Unmarshal(raw, &frames); err != nil {
			return nil, "", "", err
		}
		if len(frames) == 0 || len(frames) > mcpevent.MaxAnalysisFrames {
			return nil, "", "", fmt.Errorf("frame count is outside analysis limits")
		}
		return frames, "", "", nil
	}
	var wrapper struct {
		ProfileType string          `json:"profile_type"`
		Profile     json.RawMessage `json:"profile"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, "", "", err
	}
	if len(wrapper.Profile) == 0 {
		return nil, "", "", fmt.Errorf("flamedata is not a frame array or pprof object")
	}
	var profile profilev1.Profile
	if err := protojson.Unmarshal(wrapper.Profile, &profile); err != nil {
		return nil, "", "", fmt.Errorf("invalid pprof JSON")
	}
	return profileFrames(ctx, &profile, wrapper.ProfileType)
}

func profileFrames(ctx context.Context, profile *profilev1.Profile, profileType string) ([]flamegraph.FrameData, string, string, error) {
	if len(profile.Sample) == 0 || len(profile.SampleType) == 0 || len(profile.Sample) > mcpevent.MaxAnalysisFrames || len(profile.Location) > mcpevent.MaxAnalysisFrames || len(profile.Function) > mcpevent.MaxAnalysisFrames {
		return nil, "", "", fmt.Errorf("pprof tables are empty or exceed analysis limits")
	}
	getString := func(index int64) (string, error) {
		if index < 0 || index >= int64(len(profile.StringTable)) {
			return "", fmt.Errorf("pprof string reference is invalid")
		}
		return profile.StringTable[index], nil
	}
	wanted := ""
	if parts := strings.Split(profileType, ":"); len(parts) >= 2 {
		wanted = parts[1]
	}
	if wanted == "" && profile.DefaultSampleType != 0 {
		var err error
		wanted, err = getString(profile.DefaultSampleType)
		if err != nil {
			return nil, "", "", err
		}
	}
	selected := len(profile.SampleType) - 1
	if wanted != "" {
		selected = -1
		for index, sampleType := range profile.SampleType {
			if sampleType == nil {
				continue
			}
			name, err := getString(sampleType.Type)
			if err != nil {
				return nil, "", "", err
			}
			if name == wanted {
				selected = index
				break
			}
		}
	}
	if selected < 0 || profile.SampleType[selected] == nil {
		return nil, "", "", fmt.Errorf("pprof sample type is missing")
	}
	sampleType, err := getString(profile.SampleType[selected].Type)
	if err != nil {
		return nil, "", "", err
	}
	sampleUnit, err := getString(profile.SampleType[selected].Unit)
	if err != nil {
		return nil, "", "", err
	}
	functions := make(map[uint64]string, len(profile.Function))
	for _, function := range profile.Function {
		if function == nil {
			continue
		}
		name, err := getString(function.Name)
		if err != nil {
			return nil, "", "", err
		}
		functions[function.Id] = name
	}
	locations := make(map[uint64]*profilev1.Location, len(profile.Location))
	for _, location := range profile.Location {
		if location != nil {
			locations[location.Id] = location
		}
	}
	root := &profileNode{name: ProfileRootLabel, children: make(map[string]*profileNode)}
	nodes := 1
	for _, sample := range profile.Sample {
		if err := ctx.Err(); err != nil {
			return nil, "", "", err
		}
		if sample == nil || selected >= len(sample.Value) {
			return nil, "", "", fmt.Errorf("pprof sample values are incomplete")
		}
		value := sample.Value[selected]
		if value < 0 {
			return nil, "", "", fmt.Errorf("negative pprof weights are unsupported")
		}
		if value == 0 {
			continue
		}
		if len(sample.LocationId) > MaxProfileSourceDepth {
			return nil, "", "", fmt.Errorf("pprof stack exceeds analysis depth")
		}
		stack := make([]string, 0, len(sample.LocationId))
		for _, id := range sample.LocationId {
			location := locations[id]
			if location == nil {
				return nil, "", "", fmt.Errorf("pprof location reference is invalid")
			}
			for _, line := range location.Line {
				if line == nil {
					continue
				}
				name, ok := functions[line.FunctionId]
				if !ok {
					return nil, "", "", fmt.Errorf("pprof function reference is invalid")
				}
				if name != "" {
					stack = append(stack, name)
				}
			}
			if len(location.Line) == 0 {
				stack = append(stack, fmt.Sprintf("0x%x", location.Address))
			}
			if len(stack) > MaxProfileSourceDepth {
				return nil, "", "", fmt.Errorf("pprof inline stack exceeds analysis depth")
			}
		}
		if len(stack) == 0 {
			return nil, "", "", fmt.Errorf("pprof sample has no symbolized stack")
		}
		if value > math.MaxInt64-root.value {
			return nil, "", "", fmt.Errorf("pprof sample weight overflows")
		}
		root.value += value
		node := root
		for index := len(stack) - 1; index >= 0; index-- {
			name := stack[index]
			child := node.children[name]
			if child == nil {
				nodes++
				if nodes > mcpevent.MaxAnalysisFrames {
					return nil, "", "", fmt.Errorf("pprof tree exceeds frame limit")
				}
				child = &profileNode{name: name, children: make(map[string]*profileNode)}
				node.children[name] = child
			}
			child.value += value
			node = child
		}
		node.self += value
	}
	if root.value == 0 {
		return nil, "", "", fmt.Errorf("pprof contains no positive samples")
	}
	type pendingNode struct {
		node  *profileNode
		level int64
	}
	pending := []pendingNode{{node: root}}
	frames := make([]flamegraph.FrameData, 0, nodes)
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, "", "", err
		}
		item := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		frames = append(frames, flamegraph.FrameData{Level: item.level, Label: item.node.name, Value: item.node.value, Self: item.node.self})
		var names []string
		for name := range item.node.children {
			names = append(names, name)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		for _, name := range names {
			pending = append(pending, pendingNode{node: item.node.children[name], level: item.level + 1})
		}
	}
	return frames, sampleType, sampleUnit, nil
}

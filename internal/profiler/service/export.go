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

package service

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"huatuo-bamai/internal/profiler/output/flamegraph"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	"github.com/grafana/pyroscope/pkg/pprof"
)

// SelectMergePprof returns the standard pprof profile selected by the same
// profile type, time range, and Prometheus label selector used by Grafana.
func SelectMergePprof(req *querierv1.SelectMergeStacktracesRequest) (*profilev1.Profile, error) {
	if req == nil {
		return nil, invalidProfileQueryf("request is nil")
	}
	selection, _, err := buildProfileSelection(
		req.ProfileTypeID,
		req.LabelSelector,
		req.Start,
		req.End,
		profileQueryLimit,
	)
	if err != nil {
		return nil, err
	}
	documents, err := searchProfileDocuments(selection)
	if err != nil {
		return nil, err
	}
	if len(documents) == 0 {
		return nil, fmt.Errorf("no profiles documents found")
	}

	var merged pprof.ProfileMerge
	for _, document := range documents {
		profile := document.TracerData.Flamedata.Profile.CloneVT()
		// PeriodType is optional in pprof, while Pyroscope's merge helper
		// requires a non-nil value type even when both inputs omit it.
		if profile.PeriodType == nil {
			profile.PeriodType = &profilev1.ValueType{}
		}
		if err := merged.Merge(profile); err != nil {
			return nil, fmt.Errorf("merge profile: %w", err)
		}
	}
	return merged.Profile(), nil
}

// MarshalPprof serializes a selected profile as a gzip-compressed standard
// pprof payload suitable for go tool pprof, Parca, and other pprof readers.
func MarshalPprof(req *querierv1.SelectMergeStacktracesRequest) ([]byte, error) {
	profile, err := SelectMergePprof(req)
	if err != nil {
		return nil, err
	}
	payload, err := pprof.Marshal(profile, true)
	if err != nil {
		return nil, fmt.Errorf("marshal pprof: %w", err)
	}
	return payload, nil
}

// RenderProfileSVG writes a standalone interactive SVG flame graph for the
// selected pprof data. This path has no Grafana or Pyroscope UI dependency.
func RenderProfileSVG(req *querierv1.SelectMergeStacktracesRequest, writer io.Writer) error {
	profile, err := SelectMergePprof(req)
	if err != nil {
		return err
	}
	parts := strings.Split(req.ProfileTypeID, ":")
	if len(parts) != 5 {
		return fmt.Errorf("invalid profile type: %q", req.ProfileTypeID)
	}
	stacks, err := profileFlamegraphStacks(profile, parts[1])
	if err != nil {
		return err
	}
	if len(stacks) == 0 {
		return fmt.Errorf("selected profile contains no positive stack samples")
	}
	if err := flamegraph.RenderStyle(stacks, writer, flamegraph.DefaultStyle); err != nil {
		return fmt.Errorf("render flamegraph SVG: %w", err)
	}
	return nil
}

func profileFlamegraphStacks(profile *profilev1.Profile, sampleType string) ([]flamegraph.Stack, error) {
	if profile == nil {
		return nil, fmt.Errorf("profile is nil")
	}
	index, err := profileSampleTypeIndex(profile.StringTable, profile.SampleType, sampleType)
	if err != nil {
		return nil, err
	}

	locations := make(map[uint64]*profilev1.Location, len(profile.Location))
	for _, location := range profile.Location {
		if location != nil {
			locations[location.Id] = location
		}
	}
	functions := make(map[uint64]*profilev1.Function, len(profile.Function))
	for _, function := range profile.Function {
		if function != nil {
			functions[function.Id] = function
		}
	}

	const stackSeparator = "\x00"
	counts := make(map[string]int64)
	stackNames := make(map[string][]string)
	for _, sample := range profile.Sample {
		if sample == nil || index >= len(sample.Value) || sample.Value[index] <= 0 {
			continue
		}
		names := profileSampleStack(profile, locations, functions, sample.LocationId)
		if len(names) == 0 {
			continue
		}
		key := strings.Join(names, stackSeparator)
		counts[key] += sample.Value[index]
		stackNames[key] = names
	}

	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	stacks := make([]flamegraph.Stack, 0, len(keys))
	for _, key := range keys {
		stacks = append(stacks, flamegraph.Stack{Names: stackNames[key], Samples: counts[key]})
	}
	return stacks, nil
}

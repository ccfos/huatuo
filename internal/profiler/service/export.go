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
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"huatuo-bamai/internal/profiler/output/flamegraph"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	"github.com/grafana/pyroscope/pkg/pprof"
)

const profileSVGStackLimit = 10000

// SelectMergePprof returns the standard pprof profile for an exact target
// selection. Broad and unsupported label queries are rejected before storage
// access.
func (s *Service) SelectMergePprof(
	ctx context.Context,
	req *querierv1.SelectMergeStacktracesRequest,
) (*profilev1.Profile, error) {
	profile, _, err := s.selectMergePprof(ctx, req)
	return profile, err
}

func (s *Service) selectMergePprof(
	ctx context.Context,
	req *querierv1.SelectMergeStacktracesRequest,
) (*profilev1.Profile, string, error) {
	if req == nil {
		return nil, "", invalidProfileQueryf("request is required")
	}
	selection, err := buildProfileSelection(
		req.ProfileTypeID,
		req.LabelSelector,
		req.Start,
		req.End,
	)
	if err != nil {
		return nil, "", err
	}
	documents, err := s.searchProfileDocuments(ctx, selection)
	if err != nil {
		return nil, "", err
	}
	if len(documents) == 0 {
		return nil, "", ErrProfilesAbsent
	}

	var merged pprof.ProfileMerge
	mergedProfiles := 0
	for _, document := range documents {
		if _, found := profileDocumentSampleTotal(
			document,
			selection.sampleType,
		); !found {
			continue
		}
		profile := document.TracerData.Flamedata.Profile.CloneVT()
		if profile == nil {
			continue
		}
		// Pyroscope's merge helper requires a value even though pprof makes it
		// optional.
		if profile.PeriodType == nil {
			profile.PeriodType = &profilev1.ValueType{}
		}
		if err := merged.Merge(profile); err != nil {
			return nil, "", fmt.Errorf("merge profile: %w", err)
		}
		mergedProfiles++
	}
	if mergedProfiles == 0 {
		return nil, "", ErrProfilesAbsent
	}
	profile := merged.Profile()
	if profile == nil {
		return nil, "", ErrProfilesAbsent
	}
	return profile, selection.sampleType, nil
}

// MarshalPprof serializes a selected profile as gzip-compressed pprof data.
func (s *Service) MarshalPprof(
	ctx context.Context,
	req *querierv1.SelectMergeStacktracesRequest,
) ([]byte, error) {
	profile, err := s.SelectMergePprof(ctx, req)
	if err != nil {
		return nil, err
	}
	payload, err := pprof.Marshal(profile, true)
	if err != nil {
		return nil, fmt.Errorf("marshal pprof: %w", err)
	}
	return payload, nil
}

// RenderProfileSVG writes a standalone interactive SVG for selected pprof
// data.
func (s *Service) RenderProfileSVG(
	ctx context.Context,
	req *querierv1.SelectMergeStacktracesRequest,
	writer io.Writer,
) error {
	if writer == nil {
		return errors.New("render profile SVG: writer is nil")
	}
	profile, sampleType, err := s.selectMergePprof(ctx, req)
	if err != nil {
		return err
	}
	stacks, err := profileFlamegraphStacks(profile, sampleType)
	if err != nil {
		return err
	}
	if len(stacks) == 0 {
		return fmt.Errorf("%w: selected profile has no positive stack samples", ErrProfilesAbsent)
	}
	if err := flamegraph.RenderStyle(stacks, writer, flamegraph.DefaultStyle); err != nil {
		return fmt.Errorf("render profile SVG: %w", err)
	}
	return nil
}

func profileFlamegraphStacks(
	profile *profilev1.Profile,
	sampleType string,
) ([]flamegraph.Stack, error) {
	if profile == nil {
		return nil, errors.New("build profile stacks: profile is nil")
	}
	index, ok := profileSampleTypeIndex(profile, sampleType)
	if !ok {
		return nil, invalidProfileQueryf("sample type %q not found", sampleType)
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
		names := profileSampleStack(
			profile,
			locations,
			functions,
			sample.LocationId,
		)
		if len(names) == 0 {
			continue
		}
		key := strings.Join(names, stackSeparator)
		if _, exists := counts[key]; !exists &&
			len(counts) >= profileSVGStackLimit {
			return nil, fmt.Errorf(
				"%w: rendered SVG exceeds %d distinct stacks; narrow the "+
					"time range or selector",
				ErrProfileQueryLimitExceeded,
				profileSVGStackLimit,
			)
		}
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
		stacks = append(stacks, flamegraph.Stack{
			Names:   stackNames[key],
			Samples: counts[key],
		})
	}
	return stacks, nil
}

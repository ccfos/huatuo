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
	"fmt"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	"github.com/grafana/pyroscope/pkg/pprof"
)

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
	var merged pprof.ProfileMerge
	mergedProfiles := 0
	_, err = s.visitProfileDocuments(ctx, selection, func(document *ProfileDocument) error {
		if _, found := profileDocumentSampleTotal(
			document,
			selection.sampleType,
		); !found {
			return nil
		}
		profile := document.TracerData.Flamedata.Profile.CloneVT()
		if profile == nil {
			return nil
		}
		// Pyroscope's merge helper requires a value even though pprof makes it
		// optional.
		if profile.PeriodType == nil {
			profile.PeriodType = &profilev1.ValueType{}
		}
		if err := merged.Merge(profile); err != nil {
			return fmt.Errorf("merge profile: %w", err)
		}
		mergedProfiles++
		return nil
	})
	if err != nil {
		return nil, "", err
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

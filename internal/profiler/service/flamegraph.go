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
	"strings"
	"time"

	"huatuo-bamai/internal/log"

	googlev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	phlaremodel "github.com/grafana/pyroscope/pkg/model"
	"github.com/grafana/pyroscope/pkg/pprof"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

type ElasticSearchConfig struct {
	Address, Username, Password, Index string
}

// Service provides profile query operations.
type Service struct {
	profileStorage *ProfileStorage
}

// NewService initializes a profile query service.
func NewService(ctx context.Context, esConfig *ElasticSearchConfig) (*Service, error) {
	profileStorage, err := NewProfileStorageContext(
		ctx,
		esConfig.Address,
		esConfig.Username,
		esConfig.Password,
		esConfig.Index,
	)
	if err != nil {
		return nil, err
	}
	return &Service{profileStorage: profileStorage}, nil
}

// Close releases profile query resources.
func (s *Service) Close(ctx context.Context) error {
	return s.profileStorage.Close(ctx)
}

// Ready verifies that profile storage can serve queries.
func (s *Service) Ready(ctx context.Context) error {
	if s == nil || s.profileStorage == nil {
		return errors.New("profile service is not initialized")
	}
	return s.profileStorage.Ready(ctx)
}

// SelectMergeStacktraces selects merge stacktraces by request.
//
//	request: querierv1.SelectMergeStacktracesRequest
//	response: querierv1.SelectMergeStacktracesResponse
func (s *Service) SelectMergeStacktraces(ctx context.Context, req *querierv1.SelectMergeStacktracesRequest) (*querierv1.SelectMergeStacktracesResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: request is required", ErrInvalidQuery)
	}
	if req.End < req.Start {
		return nil, fmt.Errorf("%w: end time precedes start time", ErrInvalidQuery)
	}
	filter := &SearchFilter{
		StartTime:   time.UnixMilli(req.Start),
		EndTime:     time.UnixMilli(req.End),
		ProfileType: req.ProfileTypeID,
		Limit:       100,
	}

	log.Debugf("SelectMergeStacktracesRequest: %+v", req)

	profileTypes := strings.Split(filter.ProfileType, ":")
	if len(profileTypes) != 5 {
		return nil, fmt.Errorf("%w: invalid profile type %q", ErrInvalidQuery, filter.ProfileType)
	}

	// labels
	labels, err := parser.ParseMetricSelector(req.LabelSelector)
	if err != nil {
		return nil, errors.Join(ErrInvalidQuery, fmt.Errorf("parse matchers: %w", err))
	}

	for _, label := range labels {
		// skip empty or "All"
		if label.Value == "" || label.Value == "all" || label.Value == "All" || label.Value == "*" {
			continue
		}

		if err := applyProfileMatcher(filter, label); err != nil {
			return nil, err
		}
	}

	if filter.ID == "" && filter.Hostname == "" && filter.ContainerID == "" && filter.ContainerHostname == "" {
		return nil, fmt.Errorf("%w: id, hostname, or container must be specified", ErrInvalidQuery)
	}

	// search
	profileDocs, err := s.profileStorage.SearchProfilesContext(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("search profiles: %w", err)
	}
	if len(profileDocs) == 0 {
		return nil, ErrProfilesAbsent
	}

	// merge profileDocs
	var profilesMerge pprof.ProfileMerge
	for _, profileDoc := range profileDocs {
		profile := &profileDoc.TracerData.Flamedata.Profile
		if err := profilesMerge.Merge(profile); err != nil {
			return nil, fmt.Errorf("merge profile: %w", err)
		}
	}
	profile := profilesMerge.Profile()
	if profile == nil {
		return nil, ErrProfilesAbsent
	}
	sampleType := profileTypes[1]

	// convert profilev1.Profile to phlaremodel.Tree
	phlaremodelTree := new(phlaremodel.Tree)

	// Find the index of the sample type we're interested in
	sampleTypeIndex := -1
	for i, st := range profile.SampleType {
		if st == nil {
			continue
		}
		if value, ok := profileString(profile.StringTable, st.Type); ok && value == sampleType {
			sampleTypeIndex = i
			break
		}
	}
	if sampleTypeIndex == -1 {
		return nil, fmt.Errorf("%w: sample type %q not found", ErrInvalidQuery, sampleType)
	}

	// Create a map for quick location lookup
	locationMap := make(map[uint64]*googlev1.Location, len(profile.Location))
	for _, loc := range profile.Location {
		if loc == nil {
			continue
		}
		locationMap[loc.Id] = loc
	}

	// Create a map for quick function lookup
	functionMap := make(map[uint64]*googlev1.Function, len(profile.Function))
	for _, fn := range profile.Function {
		if fn == nil {
			continue
		}
		functionMap[fn.Id] = fn
	}

	// Process each sample
	for _, sample := range profile.Sample {
		if sample == nil {
			continue
		}
		// Get the value for our sample type
		if len(sample.Value) <= sampleTypeIndex {
			continue
		}
		value := sample.Value[sampleTypeIndex]

		// Build stack trace string from location ids
		stack := make([]string, 0, len(sample.LocationId))
		for _, locId := range sample.LocationId {
			loc, exists := locationMap[locId]
			if !exists || len(loc.Line) == 0 {
				continue
			}

			// Get the first line entry (primary function)
			line := loc.Line[0]
			if line == nil {
				continue
			}
			fn, exists := functionMap[line.FunctionId]
			if !exists {
				continue
			}

			// Get function name from string table
			funcName, ok := profileString(profile.StringTable, fn.Name)
			if !ok {
				continue
			}
			stack = append(stack, funcName)
		}

		// Insert stack into tree (leaf is at stack[0], so we need to reverse for phlaremodel.Tree)
		if len(stack) > 0 {
			for i, j := 0, len(stack)-1; i < j; i, j = i+1, j-1 {
				stack[i], stack[j] = stack[j], stack[i]
			}
			phlaremodelTree.InsertStack(value, stack...)
		}
	}

	// convert phlaremodel.Tree to FlameGraph
	return &querierv1.SelectMergeStacktracesResponse{
		Flamegraph: phlaremodel.NewFlameGraph(phlaremodelTree, -1),
	}, nil
}

func applyProfileMatcher(filter *SearchFilter, matcher *labels.Matcher) error {
	if matcher.Type != labels.MatchEqual {
		return fmt.Errorf("%w: label %q only supports equality", ErrInvalidQuery, matcher.Name)
	}
	switch matcher.Name {
	case "id":
		filter.ID = matcher.Value
	case "hostname":
		filter.Hostname = matcher.Value
	case "container_id":
		filter.ContainerID = matcher.Value
	case "container_hostname":
		filter.ContainerHostname = matcher.Value
	case "__profile_type__":
		filter.ProfileType = matcher.Value
	default:
		return fmt.Errorf("%w: invalid label %q", ErrInvalidQuery, matcher.Name)
	}
	return nil
}

func profileString(table []string, index int64) (string, bool) {
	if index < 0 || index >= int64(len(table)) {
		return "", false
	}
	return table[index], true
}

// ProfileTypes gets profiling types.
//
//	request: querierv1.ProfileTypesRequest
//	response: querierv1.ProfileTypesResponse
func (s *Service) ProfileTypes(ctx context.Context, req *querierv1.ProfileTypesRequest) (*querierv1.ProfileTypesResponse, error) {
	filter := &SearchFilter{
		StartTime: time.UnixMilli(req.Start),
		EndTime:   time.UnixMilli(req.End),
		Limit:     500,
	}

	types, err := s.profileStorage.AggregationsByFieldContext(ctx, filter, "tracer_data.flamedata.profile_type")
	if err != nil {
		return nil, fmt.Errorf("get profile types: %w", err)
	}

	resp := &querierv1.ProfileTypesResponse{}
	for _, t := range types {
		ts := strings.Split(t, ":")
		if len(ts) != 5 {
			continue
		}

		resp.ProfileTypes = append(resp.ProfileTypes, &typesv1.ProfileType{
			ID:         t,
			Name:       ts[0],
			SampleType: ts[1],
			SampleUnit: ts[2],
			PeriodType: ts[3],
			PeriodUnit: ts[4],
		})
	}

	return resp, nil
}

// SelectSeries selects series by request.
//
//	request: querierv1.SelectSeriesRequest
//	response: querierv1.SelectSeriesResponse
//
// LabelNames gets label names by request.
//
//	request: typesv1.LabelNamesRequest
//	response: typesv1.LabelNamesResponse
func (s *Service) LabelNames(context.Context, *typesv1.LabelNamesRequest) (*typesv1.LabelNamesResponse, error) {
	response := &typesv1.LabelNamesResponse{
		Names: []string{"region", "hostname", "container_id", "container_hostname", "container_host_namespace"},
	}
	return response, nil
}

// LabelValues gets label values by request.
//
//	request: typesv1.LabelValuesRequest
//	response: typesv1.LabelValuesResponse
func (s *Service) LabelValues(ctx context.Context, req *typesv1.LabelValuesRequest) (*typesv1.LabelValuesResponse, error) {
	filter := &SearchFilter{
		StartTime: time.UnixMilli(req.Start),
		EndTime:   time.UnixMilli(req.End),
		Limit:     100,
	}

	matchers, err := parser.ParseMetricSelectors(req.Matchers)
	if err != nil {
		return nil, errors.Join(ErrInvalidQuery, fmt.Errorf("parse matchers: %w", err))
	}
	if len(matchers) != 1 {
		return nil, fmt.Errorf("%w: exactly one matcher group is required", ErrInvalidQuery)
	}

	// filter: ProfileType
	profileTypePresent := false
	for _, ms := range matchers {
		for _, m := range ms {
			if err := applyProfileMatcher(filter, m); err != nil {
				return nil, err
			}
			if m.Name == "__profile_type__" {
				profileTypePresent = true
			}
		}
	}

	if !profileTypePresent {
		return nil, fmt.Errorf("%w: no __profile_type__ matcher present", ErrInvalidQuery)
	}

	names, err := s.profileStorage.AggregationsByFieldContext(ctx, filter, req.Name)
	if err != nil {
		return nil, fmt.Errorf("get profile types: %w", err)
	}

	return &typesv1.LabelValuesResponse{Names: names}, nil
}

// GetProfilesByTracerID gets all profiles by tracer_id from ES
func (s *Service) GetProfilesByTracerID(ctx context.Context, tracerID string) ([]*ProfileDocument, error) {
	return s.GetProfilesByTracerIDPage(ctx, tracerID, 1000, 0)
}

// GetProfilesByTracerIDPage gets one stable page of profiles by tracer ID.
func (s *Service) GetProfilesByTracerIDPage(ctx context.Context, tracerID string, limit, offset int) ([]*ProfileDocument, error) {
	filter := &SearchFilter{
		TracerID:  tracerID,
		StartTime: time.Now().Add(-90 * 24 * time.Hour),
		EndTime:   time.Now(),
		Limit:     limit,
		Offset:    offset,
	}

	return s.profileStorage.SearchProfilesContext(ctx, filter)
}

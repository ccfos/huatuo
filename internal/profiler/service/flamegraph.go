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
	"strings"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"

	googlev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	phlaremodel "github.com/grafana/pyroscope/pkg/model"
	"github.com/grafana/pyroscope/pkg/pprof"
	"github.com/prometheus/prometheus/promql/parser"
)

var profileStorage *ProfileStorage

type ElasticSearchConfig struct {
	Debug                              bool
	Address, Username, Password, Index string
}

// InitializeProfileFlamegraph initializes profiling flamegraph.
func InitializeProfileFlamegraph(esConfig *ElasticSearchConfig) (err error) {
	profileStorage, err = NewProfileStorage(
		esConfig.Address,
		esConfig.Username,
		esConfig.Password,
		esConfig.Index,
	)
	return err
}

// SelectMergeStacktraces selects merge stacktraces by request.
//
//	request: querierv1.SelectMergeStacktracesRequest
//	response: querierv1.SelectMergeStacktracesResponse
func SelectMergeStacktraces(req *querierv1.SelectMergeStacktracesRequest) (*querierv1.SelectMergeStacktracesResponse, error) {
	filter := &SearchFilter{
		StartTime:   time.UnixMilli(req.Start),
		EndTime:     time.UnixMilli(req.End),
		ProfileType: req.ProfileTypeID,
		Limit:       100,
	}

	log.Debugf("SelectMergeStacktracesRequest: %+v", req)

	profileTypes := strings.Split(filter.ProfileType, ":")
	if len(profileTypes) != 5 {
		return nil, fmt.Errorf("invalid profile type: %q", filter.ProfileType)
	}

	// labels
	labels, err := parser.ParseMetricSelector(req.LabelSelector)
	if err != nil {
		return nil, fmt.Errorf("parse matchers: %w", err)
	}

	for _, label := range labels {
		// skip empty or "All"
		if label.Value == "" || label.Value == "all" || label.Value == "All" || label.Value == "*" {
			continue
		}

		switch label.Name {
		case "id":
			filter.ID = label.Value
		case "hostname":
			filter.Hostname = label.Value
		case "container_hostname":
			filter.ContainerHostname = label.Value
		default:
			if !profiler.IsCollectionDimensionLabel(label.Name) {
				return nil, fmt.Errorf("invalid label: %q", label.Name)
			}
			if filter.Labels == nil {
				filter.Labels = make(map[string]string)
			}
			filter.Labels[label.Name] = label.Value
		}
	}

	if filter.ID == "" && filter.Hostname == "" && filter.ContainerHostname == "" && len(filter.Labels) == 0 {
		return nil, fmt.Errorf("id, *hostname, or a profiling dimension label must be specified")
	}

	// search
	profileDocs, err := profileStorage.SearchProfiles(filter)
	if err != nil {
		return nil, fmt.Errorf("search profiles: %w", err)
	}
	if len(profileDocs) == 0 {
		return nil, fmt.Errorf("no profiles documents found")
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
	sampleType := profileTypes[1]

	// convert profilev1.Profile to phlaremodel.Tree
	phlaremodelTree := new(phlaremodel.Tree)

	// Find the index of the sample type we're interested in
	sampleTypeIndex := -1
	for i, st := range profile.SampleType {
		if profile.StringTable[st.Type] == sampleType {
			sampleTypeIndex = i
			break
		}
	}
	if sampleTypeIndex == -1 {
		return nil, fmt.Errorf("sample type not found: %q", sampleType)
	}

	// Create a map for quick location lookup
	locationMap := make(map[uint64]*googlev1.Location)
	for _, loc := range profile.Location {
		locationMap[loc.Id] = loc
	}

	// Create a map for quick function lookup
	functionMap := make(map[uint64]*googlev1.Function)
	for _, fn := range profile.Function {
		functionMap[fn.Id] = fn
	}

	// Process each sample
	for _, sample := range profile.Sample {
		// Get the value for our sample type
		if len(sample.Value) <= sampleTypeIndex {
			continue
		}
		value := sample.Value[sampleTypeIndex]

		// Build stack trace string from location ids
		var stack []string
		for _, locId := range sample.LocationId {
			loc, exists := locationMap[locId]
			if !exists || len(loc.Line) == 0 {
				continue
			}

			// Get the first line entry (primary function)
			line := loc.Line[0]
			fn, exists := functionMap[line.FunctionId]
			if !exists {
				continue
			}

			// Get function name from string table
			funcName := profile.StringTable[fn.Name]
			stack = append(stack, funcName)
		}

		// Insert stack into tree (leaf is at stack[0], so we need to reverse for phlaremodel.Tree)
		if len(stack) > 0 {
			reversedStack := make([]string, len(stack))
			for i, j := 0, len(stack)-1; i < len(stack); i, j = i+1, j-1 {
				reversedStack[i] = stack[j]
			}
			phlaremodelTree.InsertStack(value, reversedStack...)
		}
	}

	// convert phlaremodel.Tree to FlameGraph
	return &querierv1.SelectMergeStacktracesResponse{
		Flamegraph: phlaremodel.NewFlameGraph(phlaremodelTree, -1),
	}, nil
}

// ProfileTypes gets profiling types.
//
//	request: querierv1.ProfileTypesRequest
//	response: querierv1.ProfileTypesResponse
func ProfileTypes(req *querierv1.ProfileTypesRequest) (*querierv1.ProfileTypesResponse, error) {
	filter := &SearchFilter{
		StartTime: time.UnixMilli(req.Start),
		EndTime:   time.UnixMilli(req.End),
		Limit:     500,
	}

	types, err := profileStorage.AggregationsByField(filter, "tracer_data.flamedata.profile_type")
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
func SelectSeries(req *querierv1.SelectSeriesRequest) (*querierv1.SelectSeriesResponse, error) {
	return nil, nil
}

// LabelNames gets label names by request.
//
//	request: typesv1.LabelNamesRequest
//	response: typesv1.LabelNamesResponse
func LabelNames(req *typesv1.LabelNamesRequest) (*typesv1.LabelNamesResponse, error) {
	names := []string{"region", "hostname", "container_hostname", "container_host_namespace"}
	names = append(names, profiler.CollectionDimensionLabels...)
	response := &typesv1.LabelNamesResponse{
		Names: names,
	}
	return response, nil
}

// LabelValues gets label values by request.
//
//	request: typesv1.LabelValuesRequest
//	response: typesv1.LabelValuesResponse
func LabelValues(req *typesv1.LabelValuesRequest) (*typesv1.LabelValuesResponse, error) {
	filter := &SearchFilter{
		StartTime: time.UnixMilli(req.Start),
		EndTime:   time.UnixMilli(req.End),
		Limit:     100,
	}

	matchers, err := parser.ParseMetricSelectors(req.Matchers)
	if err != nil {
		return nil, fmt.Errorf("parse matchers: %w", err)
	}

	// filter: ProfileType
	profileTypePresent := false
	for _, ms := range matchers {
		for _, m := range ms {
			switch {
			case m.Name == "__profile_type__":
				profileTypePresent = true
				filter.ProfileType = m.Value
			case profiler.IsCollectionDimensionLabel(m.Name) && m.Value != "" && m.Value != "*":
				if filter.Labels == nil {
					filter.Labels = make(map[string]string)
				}
				filter.Labels[m.Name] = m.Value
			}
		}
	}

	if !profileTypePresent {
		return nil, fmt.Errorf("no __profile_type__ matcher present")
	}

	names, err := profileStorage.AggregationsByField(filter, req.Name)
	if err != nil {
		return nil, fmt.Errorf("get profile types: %w", err)
	}

	return &typesv1.LabelValuesResponse{Names: names}, nil
}

// GetProfilesByTracerID gets all profiles by tracer_id from ES
func GetProfilesByTracerID(tracerID string) ([]*ProfileDocument, error) {
	filter := &SearchFilter{
		TracerID:  tracerID,
		StartTime: time.Now().Add(-90 * 24 * time.Hour),
		EndTime:   time.Now(),
		Limit:     1000,
	}

	return profileStorage.SearchProfiles(filter)
}

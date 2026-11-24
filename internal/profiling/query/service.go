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

package query

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ccfos/huatuo/internal/log"
	"github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/observation"
	"github.com/ccfos/huatuo/pkg/profiling"
	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"

	googlev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	phlaremodel "github.com/grafana/pyroscope/pkg/model"
	"github.com/grafana/pyroscope/pkg/pprof"
	"github.com/prometheus/prometheus/model/labels"
)

// The extra probe record detects truncation before a partial flamegraph is published.
const maxProfileQueryRecords = 100000

// ProfileQueryService provides Pyroscope-compatible profile queries.
type ProfileQueryService struct {
	profileStorage *profilingstore.Store
}

// NewProfileQueryService initializes a profile query service.
func NewProfileQueryService(profileStorage *profilingstore.Store) (*ProfileQueryService, error) {
	if profileStorage == nil {
		return nil, errors.New("create profile query service: profile storage is required")
	}
	return &ProfileQueryService{profileStorage: profileStorage}, nil
}

// SelectMergeStacktraces selects merge stacktraces by request.
//
//	request: querierv1.SelectMergeStacktracesRequest
//	response: querierv1.SelectMergeStacktracesResponse
func (s *ProfileQueryService) SelectMergeStacktraces(ctx context.Context, req *querierv1.SelectMergeStacktracesRequest) (*querierv1.SelectMergeStacktracesResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: request is required", ErrInvalidQuery)
	}

	if req.End < req.Start {
		return nil, fmt.Errorf("%w: end time precedes start time", ErrInvalidQuery)
	}

	log.Debugf("SelectMergeStacktracesRequest: %+v", req)

	profileTypes, err := window.Metadata(req.ProfileTypeID)
	if err != nil {
		return nil, errors.Join(ErrInvalidQuery, err)
	}

	selector, err := parseProfileSelector(req.LabelSelector, req.ProfileTypeID)
	if err != nil {
		return nil, err
	}

	filter := &selector.documentFilter
	filter.StartTime = time.UnixMilli(req.Start)
	filter.EndTime = time.UnixMilli(req.End)

	if filter.Scope == observation.ScopeTypeUnknown {
		return nil, fmt.Errorf("%w: scope must be host or container", ErrInvalidQuery)
	}

	if err := validateProfileHostScope(filter); err != nil {
		return nil, err
	}

	if filter.Scope == observation.ScopeTypeContainer && filter.ContainerID == "" && filter.ID == "" {
		return nil, fmt.Errorf("%w: container scope requires a container id or task id", ErrInvalidQuery)
	}

	latestOnly := filter.ProfileType == profiling.ProfileTypePhysicalUsage
	if latestOnly && filter.ID == "" {
		return nil, fmt.Errorf("%w: physical usage requires a single task id", ErrInvalidQuery)
	}

	if filter.ID == "" && filter.Hostname == "" && filter.ContainerID == "" && filter.ContainerHostname == "" {
		return nil, fmt.Errorf("%w: id, hostname, or container must be specified", ErrInvalidQuery)
	}

	var profilesMerge pprof.ProfileMerge
	var sampleCount int
	var latest *googlev1.Profile
	err = s.visitSelectedProfiles(ctx, filter, func(profile *googlev1.Profile) error {
		if len(selector.sampleMatchers) > 0 {
			// The merger does not distinguish numeric label values. Filter before
			// merging, using this query's exclusively owned decoded samples.
			selected := profile.Sample[:0]
			for _, sample := range profile.Sample {
				if err := ctx.Err(); err != nil {
					return err
				}

				matched, err := matchSampleLabels(profile, sample, selector.sampleMatchers)
				if err != nil {
					return err
				}

				if matched {
					selected = append(selected, sample)
				}
			}

			profile.Sample = selected
		}
		sampleCount += len(profile.Sample)
		if latestOnly {
			latest = profile
			return nil
		}
		// Sample weights are already in final units; the merger still requires a period type.
		profile.PeriodType = &googlev1.ValueType{}
		profile.Period = 0
		if err := profilesMerge.Merge(profile); err != nil {
			return fmt.Errorf("merge profile: %w", err)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	if sampleCount == 0 {
		return &querierv1.SelectMergeStacktracesResponse{
			Flamegraph: phlaremodel.NewFlameGraph(new(phlaremodel.Tree), req.GetMaxNodes()),
		}, nil
	}

	profile := latest
	if !latestOnly {
		profile = profilesMerge.Profile()
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

		name, _ := profileString(profile.StringTable, st.Type)
		unit, _ := profileString(profile.StringTable, st.Unit)
		if name == sampleType && unit == profileTypes[2] {
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
		Flamegraph: phlaremodel.NewFlameGraph(phlaremodelTree, req.GetMaxNodes()),
	}, nil
}

func applyProfileMatcher(filter *profilingstore.Filter, matcher *labels.Matcher) error {
	if matcher.Type != labels.MatchEqual {
		return fmt.Errorf("%w: label %q only supports equality", ErrInvalidQuery, matcher.Name)
	}
	switch matcher.Name {
	case "scope":
		scope, err := observation.ParseScope(matcher.Value)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidQuery, err)
		}
		if filter.Scope != observation.ScopeTypeUnknown && filter.Scope != scope {
			return fmt.Errorf("%w: conflicting scope matchers", ErrInvalidQuery)
		}
		filter.Scope = scope
	case "id":
		filter.ID = matcher.Value
	case observation.FieldRegion:
		filter.Region = matcher.Value
	case observation.FieldHostname:
		filter.Hostname = matcher.Value
	case observation.FieldContainerName:
		filter.ContainerName = matcher.Value
	case observation.FieldContainerID:
		filter.ContainerID = matcher.Value
	case observation.FieldContainerHostname:
		filter.ContainerHostname = matcher.Value
	case observation.FieldContainerNamespace:
		filter.ContainerNamespace = matcher.Value
	case observation.FieldContainerType:
		filter.ContainerType = matcher.Value
	case observation.FieldContainerQoS:
		filter.ContainerQoS = matcher.Value
	case "__profile_type__":
		filter.ProfileType = matcher.Value
	default:
		return fmt.Errorf("%w: invalid label %q", ErrInvalidQuery, matcher.Name)
	}
	return nil
}

// resolveProfileMatchers validates the selector and resolves it into filter.
//
// Contradictory matchers must not reach the store, where only the last value of
// a label would survive. Wildcard values only drop their own restriction, so
// the operator has to be validated before that skip: {hostname!="*"} is still a
// non-equality matcher the filter cannot express.
func resolveProfileMatchers(filter *profilingstore.Filter, matchers []*labels.Matcher, profileTypeID string) error {
	matcherSet := newProfileMatcherSet(filter)
	for _, matcher := range matchers {
		if matcher.Name == "__profile_type__" {
			if matcher.Type != labels.MatchEqual || matcher.Value != profileTypeID {
				return fmt.Errorf("%w: __profile_type__ must equal ProfileTypeID %q", ErrInvalidQuery, profileTypeID)
			}
			continue
		}
		if matcher.Type != labels.MatchEqual {
			return fmt.Errorf("%w: label %q only supports equality", ErrInvalidQuery, matcher.Name)
		}
		// skip empty or "All"
		if matcher.Value == "" || matcher.Value == "all" || matcher.Value == "All" || matcher.Value == "*" {
			continue
		}

		if err := matcherSet.apply(matcher); err != nil {
			return err
		}
	}

	return nil
}

// profileMatcherSet resolves label matchers into one store filter.
//
// A store filter keeps a single value per label, so applying matchers one by one
// makes the last value win: {hostname="host-a",hostname="host-b"} would silently
// select host-b. Remembering the value each label was already resolved to turns
// that contradiction into an explicit error instead of a query that matches
// hosts the caller never asked for.
type profileMatcherSet struct {
	filter  *profilingstore.Filter
	applied map[string]string
}

// newProfileMatcherSet returns an empty matcher set bound to filter.
func newProfileMatcherSet(filter *profilingstore.Filter) *profileMatcherSet {
	return &profileMatcherSet{
		filter:  filter,
		applied: make(map[string]string),
	}
}

// apply resolves one matcher into the bound filter.
//
// Repeating a label with the same value is accepted because the resulting
// filter is identical. Repeating a label with another value is rejected: the
// filter cannot express the intersection of two equalities, and keeping either
// one of them would change the query without telling the caller.
func (s *profileMatcherSet) apply(matcher *labels.Matcher) error {
	if matcher.Type != labels.MatchEqual {
		return fmt.Errorf("%w: label %q only supports equality", ErrInvalidQuery, matcher.Name)
	}
	previous, seen := s.applied[matcher.Name]
	if seen && previous != matcher.Value {
		return fmt.Errorf(
			"%w: label %q is selected with conflicting values %q and %q",
			ErrInvalidQuery, matcher.Name, previous, matcher.Value,
		)
	}

	if err := applyProfileMatcher(s.filter, matcher); err != nil {
		return err
	}
	s.applied[matcher.Name] = matcher.Value
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
func (s *ProfileQueryService) ProfileTypes(ctx context.Context, req *querierv1.ProfileTypesRequest) (*querierv1.ProfileTypesResponse, error) {
	filter := &profilingstore.Filter{
		StartTime: time.UnixMilli(req.Start),
		EndTime:   time.UnixMilli(req.End),
		Limit:     500,
	}

	types, err := s.profileStorage.Values(ctx, filter, "profile_type")
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

func validateProfileHostScope(filter *profilingstore.Filter) error {
	if filter.Scope != observation.ScopeTypeHost {
		return nil
	}

	if filter.ContainerID != "" || filter.ContainerName != "" || filter.ContainerHostname != "" ||
		filter.ContainerNamespace != "" || filter.ContainerType != "" || filter.ContainerQoS != "" {
		return fmt.Errorf("%w: host scope cannot include a container filter", ErrInvalidQuery)
	}

	return nil
}

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
	"slices"
	"strconv"
	"strings"
	"time"

	googlev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	"github.com/prometheus/prometheus/model/labels"

	"github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/observation"
	"github.com/ccfos/huatuo/pkg/profiling"
	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"
)

const (
	maxProfileQuerySamples = 1_000_000
	maxProfileQueryLabels  = 10_000_000
	maxProfileLabelValues  = 1000
)

var documentLabelNames = []string{
	"id", "scope", observation.FieldRegion, observation.FieldHostname,
	observation.FieldContainerID, observation.FieldContainerName,
	observation.FieldContainerHostname, observation.FieldContainerNamespace,
	observation.FieldContainerType, observation.FieldContainerQoS,
}

// profileLabelValue uses the same representation for discovery and matching.
func profileLabelValue(profile *googlev1.Profile, label *googlev1.Label) (string, error) {
	if label.Str != 0 {
		value, ok := profileString(profile.StringTable, label.Str)
		if !ok {
			return "", fmt.Errorf("stored sample label has invalid string index %d", label.Str)
		}

		return value, nil
	}

	unit, ok := profileString(profile.StringTable, label.NumUnit)
	if !ok {
		return "", fmt.Errorf("stored sample label has invalid unit index %d", label.NumUnit)
	}

	value := strconv.FormatInt(label.Num, 10)
	if unit != "" {
		value += " " + unit
	}

	return value, nil
}

func matchSampleLabels(profile *googlev1.Profile, sample *googlev1.Sample, matchers []*labels.Matcher) (bool, error) {
	if sample == nil {
		return false, nil
	}

	for _, matcher := range matchers {
		present, matched := false, false
		for _, label := range sample.Label {
			if label == nil {
				return false, errors.New("stored sample contains a nil label")
			}

			name, ok := profileString(profile.StringTable, label.Key)
			if !ok {
				return false, fmt.Errorf("stored sample label has invalid key index %d", label.Key)
			}

			if name != matcher.Name {
				continue
			}

			present = true
			value, err := profileLabelValue(profile, label)
			if err != nil {
				return false, err
			}

			if matcher.Matches(value) {
				matched = true
				break
			}
		}
		// A missing label is distinct from an explicitly stored empty string.
		if !matched && (present || matcher.Type != labels.MatchRegexp || !matcher.Matches("")) {
			return false, nil
		}
	}

	return true, nil
}

func (s *ProfileQueryService) visitSelectedProfiles(ctx context.Context, filter *profilingstore.Filter, visit func(*googlev1.Profile) error) error {
	var metadata []string
	var err error
	if filter.ProfileType != "" {
		metadata, err = window.Metadata(filter.ProfileType)
		if err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidQuery, err)
		}
	}

	latestOnly := filter.ProfileType == profiling.ProfileTypePhysicalUsage
	if latestOnly && filter.ID == "" {
		return fmt.Errorf("%w: physical usage requires a single task id", ErrInvalidQuery)
	}

	filter.Limit = maxProfileQueryRecords + 1
	records, samples, labelCount := 0, 0, 0
	var latest *googlev1.Profile
	err = s.profileStorage.Search(ctx, filter, func(batch []*profilingstore.Document) error {
		if len(batch) > maxProfileQueryRecords-records {
			return fmt.Errorf("%w: matching profiles exceed %d records; narrow the time range or filters", ErrInvalidQuery, maxProfileQueryRecords)
		}

		records += len(batch)
		for _, document := range batch {
			if err := ctx.Err(); err != nil {
				return err
			}

			if document.ProfileData == nil || document.ProfileData.Profile == nil {
				return errors.New("stored profile data is missing")
			}

			if filter.ProfileType != "" && document.ProfileData.ProfileType != filter.ProfileType {
				return fmt.Errorf("stored profile type %q does not match query type %q", document.ProfileData.ProfileType, filter.ProfileType)
			}

			if filter.ProfileType == "" && document.ProfileData.ProfileType == profiling.ProfileTypePhysicalUsage {
				return fmt.Errorf("%w: physical usage label queries require a profile type and single task id", ErrInvalidQuery)
			}

			profile := document.ProfileData.Profile
			if metadata != nil {
				found := false
				for _, sampleType := range profile.SampleType {
					if sampleType == nil {
						continue
					}

					name, _ := profileString(profile.StringTable, sampleType.Type)
					unit, _ := profileString(profile.StringTable, sampleType.Unit)
					if name == metadata[1] && unit == metadata[2] {
						found = true
						break
					}
				}

				if !found {
					return fmt.Errorf("stored profile lacks sample type %q with unit %q", metadata[1], metadata[2])
				}
			}
			if len(profile.Sample) > maxProfileQuerySamples-samples {
				return fmt.Errorf("%w: matching profiles exceed %d samples; narrow the time range or filters", ErrInvalidQuery, maxProfileQuerySamples)
			}

			samples += len(profile.Sample)
			for _, sample := range profile.Sample {
				if err := ctx.Err(); err != nil {
					return err
				}

				if sample == nil {
					continue
				}

				if len(sample.Label) > maxProfileQueryLabels-labelCount {
					return fmt.Errorf("%w: matching profiles exceed %d labels; narrow the time range or filters", ErrInvalidQuery, maxProfileQueryLabels)
				}

				labelCount += len(sample.Label)
				for _, label := range sample.Label {
					if label == nil {
						return errors.New("stored sample contains a nil label")
					}

					name, ok := profileString(profile.StringTable, label.Key)
					if !ok || name == "" {
						return fmt.Errorf("stored sample label has invalid key index %d", label.Key)
					}

					if _, err := profileLabelValue(profile, label); err != nil {
						return err
					}
				}
			}

			if latestOnly {
				if latest == nil || profile.TimeNanos > latest.TimeNanos {
					latest = profile
				}
			} else if err := visit(profile); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("query profiles: %w", err)
	}

	if records == 0 {
		return ErrProfilesAbsent
	}

	if latest != nil {
		return visit(latest)
	}

	return nil
}

func parseLabelSelector(matchers []string, start, end int64) (*profileSelector, error) {
	if end < start {
		return nil, fmt.Errorf("%w: end time precedes start time", ErrInvalidQuery)
	}

	if len(matchers) != 1 {
		return nil, fmt.Errorf("%w: exactly one matcher group is required", ErrInvalidQuery)
	}

	selector, err := parseProfileSelector(matchers[0], "")
	if err != nil {
		return nil, err
	}

	if err := validateProfileHostScope(&selector.documentFilter); err != nil {
		return nil, err
	}

	selector.documentFilter.StartTime = time.UnixMilli(start)
	selector.documentFilter.EndTime = time.UnixMilli(end)
	return selector, nil
}

// LabelNames preserves the empty-request capability list for existing clients.
func (s *ProfileQueryService) LabelNames(ctx context.Context, req *typesv1.LabelNamesRequest) (*typesv1.LabelNamesResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: request is required", ErrInvalidQuery)
	}

	if len(req.Matchers) == 0 {
		return &typesv1.LabelNamesResponse{Names: slices.Clone(documentLabelNames)}, nil
	}

	selector, err := parseLabelSelector(req.Matchers, req.Start, req.End)
	if err != nil {
		return nil, err
	}

	names, err := s.collectSampleLabels(ctx, selector, "")
	if err != nil {
		return nil, err
	}

	names = append(names, documentLabelNames...)
	slices.Sort(names)
	return &typesv1.LabelNamesResponse{Names: slices.Compact(names)}, nil
}

func (s *ProfileQueryService) LabelValues(ctx context.Context, req *typesv1.LabelValuesRequest) (*typesv1.LabelValuesResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("%w: request is required", ErrInvalidQuery)
	}

	if req.Name == "" {
		return nil, fmt.Errorf("%w: label name is required", ErrInvalidQuery)
	}

	selector, err := parseLabelSelector(req.Matchers, req.Start, req.End)
	if err != nil {
		return nil, err
	}

	if selector.documentFilter.ProfileType == "" {
		return nil, fmt.Errorf("%w: no __profile_type__ matcher present", ErrInvalidQuery)
	}

	if req.Name == "scope" {
		return &typesv1.LabelValuesResponse{Names: []string{string(observation.ScopeTypeHost), string(observation.ScopeTypeContainer)}}, nil
	}

	if isDocumentLabel(req.Name) {
		if len(selector.sampleMatchers) != 0 {
			return nil, fmt.Errorf("%w: document label values cannot be constrained by sample labels", ErrInvalidQuery)
		}

		selector.documentFilter.Limit = 100
		names, err := s.profileStorage.Values(ctx, &selector.documentFilter, req.Name)
		if err != nil {
			return nil, fmt.Errorf("get document label values: %w", err)
		}

		return &typesv1.LabelValuesResponse{Names: names}, nil
	}

	if strings.HasPrefix(req.Name, "__") {
		return nil, fmt.Errorf("%w: unsupported reserved label %q", ErrInvalidQuery, req.Name)
	}

	names, err := s.collectSampleLabels(ctx, selector, req.Name)
	if err != nil {
		return nil, err
	}

	return &typesv1.LabelValuesResponse{Names: names}, nil
}

// collectSampleLabels discovers names when name is empty, otherwise values of name.
func (s *ProfileQueryService) collectSampleLabels(ctx context.Context, selector *profileSelector, name string) ([]string, error) {
	values := make(map[string]struct{})
	err := s.visitSelectedProfiles(ctx, &selector.documentFilter, func(profile *googlev1.Profile) error {
		for _, sample := range profile.Sample {
			if err := ctx.Err(); err != nil {
				return err
			}

			matched, err := matchSampleLabels(profile, sample, selector.sampleMatchers)
			if err != nil {
				return err
			}

			if !matched {
				continue
			}

			for _, label := range sample.Label {
				key, _ := profileString(profile.StringTable, label.Key)
				if isDocumentLabel(key) || strings.HasPrefix(key, "__") {
					continue
				}

				value := key
				if name != "" {
					if key != name {
						continue
					}

					value, err = profileLabelValue(profile, label)
					if err != nil {
						return err
					}
				}
				values[value] = struct{}{}
				if len(values) > maxProfileLabelValues {
					return fmt.Errorf("%w: distinct label values exceed %d; narrow the time range or filters", ErrInvalidQuery, maxProfileLabelValues)
				}
			}
		}

		return nil
	})
	if err != nil && !errors.Is(err, ErrProfilesAbsent) {
		return nil, err
	}

	names := make([]string, 0, len(values))
	for value := range values {
		names = append(names, value)
	}

	slices.Sort(names)
	return names, nil
}

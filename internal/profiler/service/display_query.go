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
	"math"
	"sort"
	"strings"
	"time"

	"huatuo-bamai/internal/profiler"

	profilev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	phlaremodel "github.com/grafana/pyroscope/pkg/model"
	"github.com/prometheus/prometheus/promql/parser"
)

const (
	profileQueryLimit    = 10000
	profileQueryPageSize = 1000
	profileSeriesLimit   = 100
	defaultProfileNodes  = 5000
	profileNodeLimit     = 10000
)

type profileSelection struct {
	filter     *SearchFilter
	sampleType string
}

func buildProfileSelection(profileType, selector string, start, end int64) (*profileSelection, error) {
	profileTypeParts := strings.Split(profileType, ":")
	if len(profileTypeParts) != 5 {
		return nil, invalidProfileQueryf("invalid profile type %q", profileType)
	}
	if end < start {
		return nil, invalidProfileQueryf("end time precedes start time")
	}

	matchers, err := parser.ParseMetricSelector(selector)
	if err != nil {
		return nil, invalidProfileQueryf("parse matchers: %v", err)
	}
	filter := &SearchFilter{
		StartTime:   time.UnixMilli(start),
		EndTime:     time.UnixMilli(end),
		ProfileType: profileType,
	}
	for _, matcher := range matchers {
		if matcher == nil {
			continue
		}
		if matcher.Name == "__profile_type__" && matcher.Value != profileType {
			return nil, invalidProfileQueryf(
				"profile type matcher %q does not match request %q",
				matcher.Value,
				profileType,
			)
		}
		if err := applyProfileMatcher(filter, matcher); err != nil {
			return nil, err
		}
	}
	if filter.ID != "" && filter.TracerID != "" && filter.ID != filter.TracerID {
		return nil, invalidProfileQueryf("id and tracer select different profiles")
	}
	if !hasExactProfileTarget(filter) {
		return nil, invalidProfileQueryf(
			"id, hostname, container_id, container_hostname, tracer, or a " +
				"collection dimension must be specified",
		)
	}
	return &profileSelection{filter: filter, sampleType: profileTypeParts[1]}, nil
}

func invalidProfileQueryf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidQuery, fmt.Sprintf(format, args...))
}

func hasExactProfileTarget(filter *SearchFilter) bool {
	if filter == nil {
		return false
	}
	if filter.ID != "" ||
		filter.Hostname != "" ||
		filter.ContainerID != "" ||
		filter.ContainerHostname != "" ||
		filter.TracerID != "" {
		return true
	}
	for _, value := range filter.Labels {
		if value != "" {
			return true
		}
	}
	return false
}

func (s *Service) searchProfileDocuments(
	ctx context.Context,
	selection *profileSelection,
) ([]*ProfileDocument, error) {
	if s == nil || s.profileStorage == nil {
		return nil, errorsProfileStorageNotInitialized()
	}
	count, err := s.profileStorage.CountProfilesContext(ctx, selection.filter)
	if err != nil {
		return nil, fmt.Errorf("count profiles: %w", err)
	}
	if count < 0 {
		return nil, fmt.Errorf("count profiles: storage returned negative count %d", count)
	}
	if count > profileQueryLimit {
		return nil, fmt.Errorf(
			"%w: matched %d documents; narrow the time range or selector to at most %d",
			ErrProfileQueryLimitExceeded,
			count,
			profileQueryLimit,
		)
	}

	documents := make([]*ProfileDocument, 0, int(count))
	for offset := 0; offset < int(count); offset += profileQueryPageSize {
		pageFilter := *selection.filter
		pageFilter.Limit = min(profileQueryPageSize, int(count)-offset)
		pageFilter.Offset = offset
		page, err := s.profileStorage.SearchProfilesContext(ctx, &pageFilter)
		if err != nil {
			return nil, fmt.Errorf("search profiles at offset %d: %w", offset, err)
		}
		if len(page) != pageFilter.Limit {
			return nil, fmt.Errorf(
				"search profiles at offset %d: storage returned %d documents, expected %d",
				offset,
				len(page),
				pageFilter.Limit,
			)
		}
		for index, document := range page {
			if document == nil {
				return nil, fmt.Errorf(
					"search profiles at offset %d: storage returned nil document at page index %d",
					offset,
					index,
				)
			}
		}
		documents = append(documents, page...)
	}
	if int64(len(documents)) != count {
		return nil, fmt.Errorf(
			"search profiles: storage returned %d documents after count reported %d",
			len(documents),
			count,
		)
	}
	return documents, nil
}

func errorsProfileStorageNotInitialized() error {
	return fmt.Errorf("profile storage is not initialized")
}

func (s *Service) selectProfileTree(
	ctx context.Context,
	req *querierv1.SelectMergeStacktracesRequest,
	allowEmpty bool,
) (*phlaremodel.Tree, bool, error) {
	selection, err := buildProfileSelection(
		req.ProfileTypeID,
		req.LabelSelector,
		req.Start,
		req.End,
	)
	if err != nil {
		return nil, false, err
	}
	documents, err := s.searchProfileDocuments(ctx, selection)
	if err != nil {
		return nil, false, err
	}
	if len(documents) == 0 && !allowEmpty {
		return nil, false, ErrProfilesAbsent
	}

	tree := new(phlaremodel.Tree)
	hasSamples := false
	for _, document := range documents {
		if addProfileDocumentToTree(tree, document, selection.sampleType) {
			hasSamples = true
		}
	}
	if !hasSamples && !allowEmpty {
		return nil, false, ErrProfilesAbsent
	}
	return tree, hasSamples, nil
}

func addProfileDocumentToTree(
	tree *phlaremodel.Tree,
	document *ProfileDocument,
	sampleType string,
) bool {
	profile := &document.TracerData.Flamedata.Profile
	sampleTypeIndex, ok := profileSampleTypeIndex(profile, sampleType)
	if !ok {
		return false
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
	inserted := false
	for _, sample := range profile.Sample {
		if sample == nil || sampleTypeIndex >= len(sample.Value) {
			continue
		}
		stack := profileSampleStack(profile, locations, functions, sample.LocationId)
		if len(stack) > 0 {
			tree.InsertStack(sample.Value[sampleTypeIndex], stack...)
			inserted = true
		}
	}
	return inserted
}

func profileSampleTypeIndex(profile *profilev1.Profile, sampleType string) (int, bool) {
	for i, valueType := range profile.SampleType {
		if valueType == nil {
			continue
		}
		if name, ok := profileString(profile.StringTable, valueType.Type); ok && name == sampleType {
			return i, true
		}
	}
	return -1, false
}

func profileSampleStack(
	profile *profilev1.Profile,
	locations map[uint64]*profilev1.Location,
	functions map[uint64]*profilev1.Function,
	locationIDs []uint64,
) []string {
	stack := make([]string, 0, len(locationIDs))
	for _, locationID := range locationIDs {
		location := locations[locationID]
		if location == nil {
			continue
		}
		for _, line := range location.Line {
			if line == nil {
				continue
			}
			function := functions[line.FunctionId]
			if function == nil {
				continue
			}
			name, ok := profileString(profile.StringTable, function.Name)
			if ok {
				stack = append(stack, name)
			}
		}
	}
	for left, right := 0, len(stack)-1; left < right; left, right = left+1, right-1 {
		stack[left], stack[right] = stack[right], stack[left]
	}
	return stack
}

// Diff compares two independently selected profile windows.
func (s *Service) Diff(
	ctx context.Context,
	req *querierv1.DiffRequest,
) (*querierv1.DiffResponse, error) {
	if req == nil || req.Left == nil || req.Right == nil {
		return nil, invalidProfileQueryf("left and right profile selections are required")
	}
	if req.Left.ProfileTypeID != req.Right.ProfileTypeID {
		return nil, invalidProfileQueryf("left and right profile types must match")
	}
	maxNodes, err := diffMaxNodes(req)
	if err != nil {
		return nil, err
	}
	left, leftFound, err := s.selectProfileTree(ctx, req.Left, true)
	if err != nil {
		return nil, fmt.Errorf("select left profiles: %w", err)
	}
	right, rightFound, err := s.selectProfileTree(ctx, req.Right, true)
	if err != nil {
		return nil, fmt.Errorf("select right profiles: %w", err)
	}
	if !leftFound && !rightFound {
		return nil, ErrProfilesAbsent
	}
	flamegraph, err := phlaremodel.NewFlamegraphDiff(left, right, maxNodes)
	if err != nil {
		return nil, fmt.Errorf("build flamegraph diff: %w", err)
	}
	return &querierv1.DiffResponse{Flamegraph: flamegraph}, nil
}

func diffMaxNodes(req *querierv1.DiffRequest) (int64, error) {
	left, err := normalizeProfileMaxNodes(req.Left.GetMaxNodes())
	if err != nil {
		return 0, fmt.Errorf("left selection: %w", err)
	}
	right, err := normalizeProfileMaxNodes(req.Right.GetMaxNodes())
	if err != nil {
		return 0, fmt.Errorf("right selection: %w", err)
	}
	switch {
	case right < left:
		return right, nil
	default:
		return left, nil
	}
}

func normalizeProfileMaxNodes(maxNodes int64) (int64, error) {
	if maxNodes == 0 {
		return defaultProfileNodes, nil
	}
	if maxNodes < 0 || maxNodes > profileNodeLimit {
		return 0, invalidProfileQueryf(
			"max nodes must be between 0 and %d",
			profileNodeLimit,
		)
	}
	return maxNodes, nil
}

// SelectSeries returns sample totals bucketed by time and exact profile labels.
func (s *Service) SelectSeries(
	ctx context.Context,
	req *querierv1.SelectSeriesRequest,
) (*querierv1.SelectSeriesResponse, error) {
	if req == nil {
		return nil, invalidProfileQueryf("request is required")
	}
	stepMillis, err := seriesStepMillis(req.Step)
	if err != nil {
		return nil, err
	}
	aggregation := typesv1.TimeSeriesAggregationType_TIME_SERIES_AGGREGATION_TYPE_SUM
	if req.Aggregation != nil {
		aggregation = *req.Aggregation
	}
	if aggregation != typesv1.TimeSeriesAggregationType_TIME_SERIES_AGGREGATION_TYPE_SUM &&
		aggregation != typesv1.TimeSeriesAggregationType_TIME_SERIES_AGGREGATION_TYPE_AVERAGE {
		return nil, invalidProfileQueryf(
			"unsupported time series aggregation %q",
			aggregation.String(),
		)
	}
	groupBy, err := normalizeProfileGroupBy(req.GroupBy)
	if err != nil {
		return nil, err
	}
	selection, err := buildProfileSelection(
		req.ProfileTypeID,
		req.LabelSelector,
		req.Start,
		req.End,
	)
	if err != nil {
		return nil, err
	}
	documents, err := s.searchProfileDocuments(ctx, selection)
	if err != nil {
		return nil, err
	}

	type bucketValue struct {
		sum   float64
		count int64
	}
	type groupedSeries struct {
		labels  []*typesv1.LabelPair
		buckets map[int64]*bucketValue
	}
	groups := make(map[string]*groupedSeries)
	for _, document := range documents {
		if document == nil {
			continue
		}
		value, found := profileDocumentSampleTotal(document, selection.sampleType)
		if !found {
			continue
		}
		seriesLabels, key := profileSeriesLabels(document, groupBy)
		group := groups[key]
		if group == nil {
			group = &groupedSeries{
				labels:  seriesLabels,
				buckets: make(map[int64]*bucketValue),
			}
			groups[key] = group
		}
		timestamp := profileDocumentTimestamp(document).UnixMilli()
		bucketTimestamp := req.Start + ((timestamp-req.Start)/stepMillis)*stepMillis
		bucket := group.buckets[bucketTimestamp]
		if bucket == nil {
			bucket = &bucketValue{}
			group.buckets[bucketTimestamp] = bucket
		}
		bucket.sum += value
		bucket.count++
	}

	series := make([]*typesv1.Series, 0, len(groups))
	for _, group := range groups {
		points := make([]*typesv1.Point, 0, len(group.buckets))
		for timestamp, value := range group.buckets {
			pointValue := value.sum
			if aggregation ==
				typesv1.TimeSeriesAggregationType_TIME_SERIES_AGGREGATION_TYPE_AVERAGE {
				pointValue /= float64(value.count)
			}
			points = append(points, &typesv1.Point{
				Value:     pointValue,
				Timestamp: timestamp,
			})
		}
		sort.Slice(points, func(i, j int) bool {
			return points[i].Timestamp < points[j].Timestamp
		})
		series = append(series, &typesv1.Series{
			Labels: group.labels,
			Points: points,
		})
	}
	sort.Slice(series, func(i, j int) bool {
		left, right := profileSeriesValue(series[i]), profileSeriesValue(series[j])
		if left != right {
			return left > right
		}
		return phlaremodel.CompareLabelPairs(series[i].Labels, series[j].Labels) < 0
	})
	if len(series) > profileSeriesLimit {
		series = series[:profileSeriesLimit]
	}
	return &querierv1.SelectSeriesResponse{Series: series}, nil
}

func seriesStepMillis(step float64) (int64, error) {
	if math.IsNaN(step) ||
		math.IsInf(step, 0) ||
		step <= 0 ||
		step > float64(math.MaxInt64)/1000 {
		return 0, invalidProfileQueryf(
			"step must be a finite positive number of seconds",
		)
	}
	milliseconds := int64(math.Round(step * 1000))
	if milliseconds < 1 {
		return 1, nil
	}
	return milliseconds, nil
}

func normalizeProfileGroupBy(groupBy []string) ([]string, error) {
	seen := make(map[string]struct{}, len(groupBy))
	result := make([]string, 0, len(groupBy))
	for _, name := range groupBy {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		switch name {
		case "id", "hostname", "container_id", "container_hostname", "tracer":
		default:
			if !profiler.IsCollectionDimensionLabel(name) {
				return nil, invalidProfileQueryf("invalid group-by label %q", name)
			}
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		result = append(result, name)
	}
	sort.Strings(result)
	return result, nil
}

func profileDocumentSampleTotal(
	document *ProfileDocument,
	sampleType string,
) (float64, bool) {
	profile := &document.TracerData.Flamedata.Profile
	index, ok := profileSampleTypeIndex(profile, sampleType)
	if !ok {
		return 0, false
	}
	var total float64
	var found bool
	for _, sample := range profile.Sample {
		if sample == nil || index >= len(sample.Value) {
			continue
		}
		total += float64(sample.Value[index])
		found = true
	}
	return total, found
}

func profileSeriesLabels(
	document *ProfileDocument,
	groupBy []string,
) ([]*typesv1.LabelPair, string) {
	pairs := make([]*typesv1.LabelPair, 0, len(groupBy))
	var key strings.Builder
	for _, name := range groupBy {
		value := profileDocumentLabelValue(document, name)
		pairs = append(pairs, &typesv1.LabelPair{Name: name, Value: value})
		_, _ = fmt.Fprintf(&key, "%d:%s=%d:%s;", len(name), name, len(value), value)
	}
	return pairs, key.String()
}

func profileDocumentLabelValue(document *ProfileDocument, name string) string {
	switch name {
	case "id", "tracer":
		return document.TracerID
	case "hostname":
		return document.Hostname
	case "container_id":
		return document.ContainerID
	case "container_hostname":
		return document.ContainerHostname
	default:
		if profiler.IsCollectionDimensionLabel(name) {
			return document.TracerData.Flamedata.Labels[name]
		}
		return ""
	}
}

func profileDocumentTimestamp(document *ProfileDocument) time.Time {
	if !document.UploadedTime.IsZero() {
		return document.UploadedTime
	}
	return parseProfileDocumentTime(document.TracerTime, time.Unix(0, 0))
}

func profileSeriesValue(series *typesv1.Series) float64 {
	var total float64
	for _, point := range series.GetPoints() {
		total += point.GetValue()
	}
	return total
}

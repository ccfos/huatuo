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
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/internal/profiler"

	googlev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	phlaremodel "github.com/grafana/pyroscope/pkg/model"
	promlabels "github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

const (
	profileQueryLimit  = 10000
	profileSeriesLimit = 10
)

var (
	// ErrInvalidProfileQuery identifies client-controlled selector, time range,
	// aggregation, and profile-type validation failures.
	ErrInvalidProfileQuery = errors.New("invalid profile query")
	// ErrProfileQueryLimitExceeded prevents returning a silently truncated
	// flame graph, series, diff, or export.
	ErrProfileQueryLimitExceeded = errors.New("profile query limit exceeded")
)

func invalidProfileQueryf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidProfileQuery, fmt.Sprintf(format, args...))
}

type profileQueryStorage interface {
	SearchProfiles(filter *SearchFilter) ([]*ProfileDocument, error)
	AggregationsByField(filter *SearchFilter, field string) ([]string, error)
}

type profileQueryCountStorage interface {
	CountProfiles(filter *SearchFilter) (int64, error)
}

var profileStorage profileQueryStorage

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
	if req == nil {
		return nil, invalidProfileQueryf("request is nil")
	}
	log.Debugf("SelectMergeStacktracesRequest: %+v", req)

	tree, err := selectProfileTree(req, false)
	if err != nil {
		return nil, err
	}

	return &querierv1.SelectMergeStacktracesResponse{
		Flamegraph: phlaremodel.NewFlameGraph(tree, req.GetMaxNodes()),
	}, nil
}

// Diff compares two independently selected profile windows.
func Diff(req *querierv1.DiffRequest) (*querierv1.DiffResponse, error) {
	if req == nil || req.Left == nil || req.Right == nil {
		return nil, invalidProfileQueryf("left and right profile selections are required")
	}
	if req.Left.ProfileTypeID != req.Right.ProfileTypeID {
		return nil, invalidProfileQueryf("left and right profile types must match")
	}

	left, err := selectProfileTree(req.Left, true)
	if err != nil {
		return nil, fmt.Errorf("select left profiles: %w", err)
	}
	right, err := selectProfileTree(req.Right, true)
	if err != nil {
		return nil, fmt.Errorf("select right profiles: %w", err)
	}

	flamegraph, err := phlaremodel.NewFlamegraphDiff(left, right, diffMaxNodes(req))
	if err != nil {
		return nil, fmt.Errorf("build flamegraph diff: %w", err)
	}
	return &querierv1.DiffResponse{Flamegraph: flamegraph}, nil
}

func diffMaxNodes(req *querierv1.DiffRequest) int64 {
	left := req.Left.GetMaxNodes()
	right := req.Right.GetMaxNodes()
	switch {
	case left > 0 && right > 0 && right < left:
		return right
	case left > 0:
		return left
	default:
		return right
	}
}

type profileSelection struct {
	filter   *SearchFilter
	matchers []*promlabels.Matcher
}

func buildProfileSelection(profileType, selector string, start, end int64, limit int) (*profileSelection, string, error) {
	profileTypeParts := strings.Split(profileType, ":")
	if len(profileTypeParts) != 5 {
		return nil, "", invalidProfileQueryf("invalid profile type: %q", profileType)
	}
	if end < start {
		return nil, "", invalidProfileQueryf("end time must not be before start time")
	}

	var matchers []*promlabels.Matcher
	var err error
	if strings.TrimSpace(selector) != "" {
		matchers, err = parser.ParseMetricSelector(selector)
		if err != nil {
			return nil, "", invalidProfileQueryf("parse matchers: %v", err)
		}
	}

	selection := &profileSelection{
		filter: &SearchFilter{
			StartTime:   time.UnixMilli(start),
			EndTime:     time.UnixMilli(end),
			ProfileType: profileType,
			Limit:       limit,
		},
		matchers: make([]*promlabels.Matcher, 0, len(matchers)),
	}
	for _, matcher := range matchers {
		if matcher == nil {
			continue
		}
		if err := validateProfileQueryLabel(matcher.Name); err != nil {
			return nil, "", err
		}
		selection.matchers = append(selection.matchers, matcher)

		if matcher.Name == "container_hostname" {
			switch matcher.Type {
			case promlabels.MatchEqual:
				selection.filter.IncludeContainerProfiles = matcher.Value != ""
			case promlabels.MatchRegexp:
				// Grafana uses ^$ for the comparison dashboard's host-level
				// selection. Other regexes must search container documents too;
				// a regex without metacharacters is safe to push down as equality.
				if matcher.Value != "^$" {
					selection.filter.IncludeContainerProfiles = true
					if regexp.QuoteMeta(matcher.Value) == matcher.Value {
						selection.filter.ContainerHostname = matcher.Value
					}
				}
			default:
				// Negative matchers need both host and container candidates so
				// their Prometheus missing-label semantics can be applied later.
				selection.filter.IncludeContainerProfiles = true
			}
		}

		// Push exact-match predicates into Elasticsearch. Regex and negative
		// matchers are evaluated after decoding so their Prometheus semantics are
		// preserved rather than silently treating them as equality predicates.
		if matcher.Type != promlabels.MatchEqual {
			continue
		}
		switch matcher.Name {
		case "id":
			selection.filter.ID = matcher.Value
		case "hostname":
			selection.filter.Hostname = matcher.Value
		case "container_hostname":
			selection.filter.ContainerHostname = matcher.Value
		default:
			if profiler.IsCollectionDimensionLabel(matcher.Name) {
				if selection.filter.Labels == nil {
					selection.filter.Labels = make(map[string]string)
				}
				selection.filter.Labels[matcher.Name] = matcher.Value
			}
		}
	}
	return selection, profileTypeParts[1], nil
}

func validateProfileQueryLabel(name string) error {
	switch name {
	case "id", "region", "hostname", "container_hostname", "container_host_namespace",
		"container_type", "container_qos", "tracer_name", "tracer_id", "tracer_type",
		"__profile_type__":
		return nil
	}
	if err := profiler.ValidateLabelName(name); err != nil {
		return invalidProfileQueryf("invalid profile query label %q: %v", name, err)
	}
	return nil
}

func searchProfileDocuments(selection *profileSelection) ([]*ProfileDocument, error) {
	if profileStorage == nil {
		return nil, fmt.Errorf("profile storage is not initialized")
	}
	if counter, ok := profileStorage.(profileQueryCountStorage); ok {
		count, err := counter.CountProfiles(selection.filter)
		if err != nil {
			return nil, fmt.Errorf("count profiles: %w", err)
		}
		if count > int64(selection.filter.Limit) {
			return nil, fmt.Errorf(
				"%w: storage matched %d documents; narrow the time range or selector below %d documents",
				ErrProfileQueryLimitExceeded,
				count,
				selection.filter.Limit,
			)
		}
	}
	documents, err := profileStorage.SearchProfiles(selection.filter)
	if err != nil {
		return nil, fmt.Errorf("search profiles: %w", err)
	}

	matched := documents[:0]
	for _, document := range documents {
		if document == nil || !profileDocumentInRange(document, selection.filter) {
			continue
		}
		matches := true
		for _, matcher := range selection.matchers {
			if !matcher.Matches(profileDocumentLabelValue(document, matcher.Name)) {
				matches = false
				break
			}
		}
		if matches {
			matched = append(matched, document)
		}
	}
	return matched, nil
}

func profileDocumentInRange(document *ProfileDocument, filter *SearchFilter) bool {
	timestamp := profileDocumentTimestamp(document)
	return (filter.StartTime.IsZero() || !timestamp.Before(filter.StartTime)) &&
		(filter.EndTime.IsZero() || !timestamp.After(filter.EndTime)) &&
		(filter.ProfileType == "" || document.TracerData.Flamedata.ProfileType == filter.ProfileType)
}

func profileDocumentTimestamp(document *ProfileDocument) time.Time {
	if !document.UploadedTime.IsZero() {
		return document.UploadedTime
	}
	return parseProfileDocumentTime(document.TracerTime, time.Unix(0, 0))
}

func profileDocumentLabelValue(document *ProfileDocument, name string) string {
	switch name {
	case "id", "tracer_id":
		return document.TracerID
	case "region":
		return document.Region
	case "hostname":
		return document.Hostname
	case "container_hostname":
		return document.ContainerHostname
	case "container_host_namespace":
		return document.ContainerHostNamespace
	case "container_type":
		return document.ContainerType
	case "container_qos":
		return document.ContainerQOS
	case "tracer_name":
		return document.TracerName
	case "tracer_type":
		return document.TracerRunType
	case "__profile_type__":
		return document.TracerData.Flamedata.ProfileType
	default:
		return document.TracerData.Flamedata.Labels[name]
	}
}

func selectProfileTree(req *querierv1.SelectMergeStacktracesRequest, allowEmpty bool) (*phlaremodel.Tree, error) {
	selection, sampleType, err := buildProfileSelection(
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
	if len(documents) == 0 && !allowEmpty {
		return nil, fmt.Errorf("no profiles documents found")
	}

	tree := new(phlaremodel.Tree)
	for _, document := range documents {
		if err := addProfileDocumentToTree(tree, document, sampleType); err != nil {
			return nil, err
		}
	}
	return tree, nil
}

func addProfileDocumentToTree(tree *phlaremodel.Tree, document *ProfileDocument, sampleType string) error {
	profile := &document.TracerData.Flamedata.Profile
	sampleTypeIndex, err := profileSampleTypeIndex(profile.StringTable, profile.SampleType, sampleType)
	if err != nil {
		return err
	}

	locationMap := make(map[uint64]*googlev1.Location, len(profile.Location))
	for _, location := range profile.Location {
		if location != nil {
			locationMap[location.Id] = location
		}
	}
	functionMap := make(map[uint64]*googlev1.Function, len(profile.Function))
	for _, function := range profile.Function {
		if function != nil {
			functionMap[function.Id] = function
		}
	}

	for _, sample := range profile.Sample {
		if sample == nil || sampleTypeIndex >= len(sample.Value) {
			continue
		}
		stack := profileSampleStack(profile, locationMap, functionMap, sample.LocationId)
		if len(stack) > 0 {
			tree.InsertStack(sample.Value[sampleTypeIndex], stack...)
		}
	}
	return nil
}

func profileSampleStack(
	profile *googlev1.Profile,
	locationMap map[uint64]*googlev1.Location,
	functionMap map[uint64]*googlev1.Function,
	locationIDs []uint64,
) []string {
	stack := make([]string, 0, len(locationIDs))
	for _, locationID := range locationIDs {
		location := locationMap[locationID]
		if location == nil {
			continue
		}
		// pprof stores leaf locations first. Within one location, Line[0]
		// is the innermost inlined function and the last line is its caller.
		for _, line := range location.Line {
			if line == nil {
				continue
			}
			function := functionMap[line.FunctionId]
			if function == nil || function.Name < 0 || function.Name >= int64(len(profile.StringTable)) {
				continue
			}
			stack = append(stack, profile.StringTable[function.Name])
		}
	}
	for left, right := 0, len(stack)-1; left < right; left, right = left+1, right-1 {
		stack[left], stack[right] = stack[right], stack[left]
	}
	return stack
}

func profileSampleTypeIndex(stringTable []string, sampleTypes []*googlev1.ValueType, sampleType string) (int, error) {
	for i, valueType := range sampleTypes {
		if valueType == nil || valueType.Type < 0 || valueType.Type >= int64(len(stringTable)) {
			continue
		}
		if stringTable[valueType.Type] == sampleType {
			return i, nil
		}
	}
	return -1, fmt.Errorf("sample type not found: %q", sampleType)
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
	if req == nil {
		return nil, invalidProfileQueryf("request is nil")
	}
	if req.End < req.Start {
		return nil, invalidProfileQueryf("end time must not be before start time")
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
		return nil, invalidProfileQueryf("unsupported time series aggregation: %s", aggregation.String())
	}

	selection, sampleType, err := buildProfileSelection(
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
		return &querierv1.SelectSeriesResponse{}, nil
	}

	groupBy, err := normalizeGroupBy(req.GroupBy)
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
		value, found, valueErr := profileDocumentSampleTotal(document, sampleType)
		if valueErr != nil {
			return nil, valueErr
		}
		if !found {
			continue
		}

		labels, key := profileSeriesLabels(document, groupBy)
		group := groups[key]
		if group == nil {
			group = &groupedSeries{labels: labels, buckets: make(map[int64]*bucketValue)}
			groups[key] = group
		}
		timestamp := profileDocumentTimestamp(document).UnixMilli()
		bucket := req.Start + ((timestamp-req.Start)/stepMillis)*stepMillis
		entry := group.buckets[bucket]
		if entry == nil {
			entry = &bucketValue{}
			group.buckets[bucket] = entry
		}
		entry.sum += float64(value)
		entry.count++
	}

	series := make([]*typesv1.Series, 0, len(groups))
	for _, group := range groups {
		points := make([]*typesv1.Point, 0, len(group.buckets))
		for timestamp, value := range group.buckets {
			pointValue := value.sum
			if aggregation == typesv1.TimeSeriesAggregationType_TIME_SERIES_AGGREGATION_TYPE_AVERAGE {
				pointValue /= float64(value.count)
			}
			points = append(points, &typesv1.Point{Timestamp: timestamp, Value: pointValue})
		}
		sort.Slice(points, func(i, j int) bool { return points[i].Timestamp < points[j].Timestamp })
		series = append(series, &typesv1.Series{Labels: group.labels, Points: points})
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

func profileSeriesValue(series *typesv1.Series) float64 {
	var total float64
	for _, point := range series.GetPoints() {
		total += point.GetValue()
	}
	return total
}

func seriesStepMillis(step float64) (int64, error) {
	if math.IsNaN(step) || math.IsInf(step, 0) || step <= 0 || step > float64(math.MaxInt64)/1000 {
		return 0, invalidProfileQueryf("step must be a finite positive number of seconds")
	}
	milliseconds := int64(math.Round(step * 1000))
	if milliseconds < 1 {
		milliseconds = 1
	}
	return milliseconds, nil
}

func normalizeGroupBy(groupBy []string) ([]string, error) {
	seen := make(map[string]struct{}, len(groupBy))
	result := make([]string, 0, len(groupBy))
	for _, name := range groupBy {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if err := validateProfileQueryLabel(name); err != nil {
			return nil, err
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

func profileDocumentSampleTotal(document *ProfileDocument, sampleType string) (int64, bool, error) {
	profile := &document.TracerData.Flamedata.Profile
	index, err := profileSampleTypeIndex(profile.StringTable, profile.SampleType, sampleType)
	if err != nil {
		return 0, false, err
	}
	var total int64
	var found bool
	for _, sample := range profile.Sample {
		if sample == nil || index >= len(sample.Value) {
			continue
		}
		total += sample.Value[index]
		found = true
	}
	return total, found, nil
}

func profileSeriesLabels(document *ProfileDocument, groupBy []string) ([]*typesv1.LabelPair, string) {
	labels := make([]*typesv1.LabelPair, 0, len(groupBy))
	for _, name := range groupBy {
		labels = append(labels, &typesv1.LabelPair{
			Name:  name,
			Value: profileDocumentLabelValue(document, name),
		})
	}
	return labels, labelPairsKey(labels)
}

func labelPairsKey(labels []*typesv1.LabelPair) string {
	var key strings.Builder
	for _, label := range labels {
		if label == nil {
			continue
		}
		_, _ = fmt.Fprintf(&key, "%d:%s=%d:%s;", len(label.Name), label.Name, len(label.Value), label.Value)
	}
	return key.String()
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
		return nil, invalidProfileQueryf("parse matchers: %v", err)
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
		return nil, invalidProfileQueryf("no __profile_type__ matcher present")
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

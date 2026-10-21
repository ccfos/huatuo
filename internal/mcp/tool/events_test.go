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

package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/flamegraph"
	tracingstore "github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/constant"
	"github.com/ccfos/huatuo/pkg/types"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
)

type fakeEventStore struct {
	backendType      string
	tracerNamesCalls int
	tracerLimits     []int
	tracerFilters    []*tracingstore.Query
	searches         [][]string
	searchFilters    []*tracingstore.Query
	searchDocuments  []*tracingstore.Document
	counts           map[string]int64
	searchErrors     map[string]error
	partialDocuments map[string][]*tracingstore.Document
	countCalls       [][]string
	countError       error
	searchWarnings   []string
}

func (s *fakeEventStore) BackendType() string {
	if s.backendType == "" {
		return constant.EventBackendElasticsearch
	}
	return s.backendType
}

func (s *fakeEventStore) SearchEvents(ctx context.Context, filter *tracingstore.Query) (tracingstore.SearchResult, error) {
	documents, err := s.Search(ctx, filter)
	result := tracingstore.SearchResult{Documents: documents, Warnings: s.searchWarnings}
	if filter.Limit > 0 && len(documents) > filter.Limit {
		result.Documents = documents[:filter.Limit]
		result.Truncated = true
	}
	return result, err
}

func (s *fakeEventStore) Search(
	_ context.Context,
	filter *tracingstore.Query,
) ([]*tracingstore.Document, error) {
	tracers := append([]string(nil), filter.TracerNames...)
	s.searches = append(s.searches, tracers)
	s.searchFilters = append(s.searchFilters, copyEventFilter(filter))
	key := stringsKey(tracers)
	if err := s.searchErrors[key]; err != nil {
		return s.partialDocuments[key], err
	}
	if s.searchDocuments != nil {
		if filter.RecordID != "" {
			matching := make([]*tracingstore.Document, 0, 1)
			for _, document := range s.searchDocuments {
				if document != nil && document.RecordID == filter.RecordID {
					matching = append(matching, document)
				}
			}
			return matching, nil
		}
		return s.searchDocuments, nil
	}
	return []*tracingstore.Document{{}}, nil
}

func (s *fakeEventStore) Count(
	_ context.Context,
	filter *tracingstore.Query,
) (int64, error) {
	s.countCalls = append(s.countCalls, append([]string(nil), filter.TracerNames...))
	if s.countError != nil {
		return 0, s.countError
	}
	if len(filter.TracerNames) > 1 {
		var total int64
		for _, tracer := range filter.TracerNames {
			total += s.counts[tracer]
		}
		return total, nil
	}
	return s.counts[filter.TracerNames[0]], nil
}

type countingFakeEventStore struct {
	*fakeEventStore
	tracerCounts      []tracingstore.TracerCount
	tracerCountsErr   error
	tracerCountsCalls int
	tracerCountFilter *tracingstore.Query
}

type sizedFakeEventStore struct {
	*fakeEventStore
	searchLimits []int
}

func (s *sizedFakeEventStore) SearchEvents(ctx context.Context, filter *tracingstore.Query) (tracingstore.SearchResult, error) {
	documents, err := s.Search(ctx, filter)
	return tracingstore.SearchResult{Documents: documents}, err
}

func (s *sizedFakeEventStore) Search(
	_ context.Context,
	filter *tracingstore.Query,
) ([]*tracingstore.Document, error) {
	s.searchLimits = append(s.searchLimits, filter.Limit)
	count := int64(0)
	for _, tracer := range filter.TracerNames {
		count += s.counts[tracer]
	}
	documents := make([]*tracingstore.Document, min(int(count), filter.Limit))
	for index := range documents {
		documents[index] = &tracingstore.Document{}
	}
	return documents, nil
}

func (s *countingFakeEventStore) TracerCounts(
	_ context.Context,
	filter *tracingstore.Query,
) ([]tracingstore.TracerCount, error) {
	s.tracerCountsCalls++
	s.tracerCountFilter = copyEventFilter(filter)
	return s.tracerCounts, s.tracerCountsErr
}

func (s *fakeEventStore) TracerNames(
	_ context.Context,
	query *tracingstore.Query,
) ([]string, error) {
	s.tracerNamesCalls++
	s.tracerFilters = append(s.tracerFilters, copyEventFilter(query))
	s.tracerLimits = append(s.tracerLimits, query.Limit)
	if query.ProfileOnly {
		return []string{"cpusys"}, nil
	}
	return []string{"waitrate", "cpusys"}, nil
}

func TestDiscoverTracersUsesCompleteCatalogLimit(t *testing.T) {
	store := &fakeEventStore{}
	router := testEventRouter(t, store)
	tools := &eventTools{
		stores:               router,
		tracerCache:          make(map[string]tracerCacheEntry),
		tracerCacheTTL:       time.Minute,
		maxDocumentsPerQuery: 37,
	}
	result, err := tools.discoverTracers(t.Context(), eventToolRequest(map[string]any{
		constant.ArgumentRegion:    []any{"cn"},
		constant.ArgumentTimeRange: "1h",
	}))
	if err != nil || result.IsError {
		t.Fatalf("discoverTracers() = %+v, %v", result, err)
	}
	if !reflect.DeepEqual(store.tracerLimits, []int{1001, 1001}) {
		t.Fatalf("tracer limits = %v, want [1001 1001]", store.tracerLimits)
	}
	for _, query := range store.tracerFilters {
		if query.Region != "cn" || query.EndTime.Sub(query.StartTime) != time.Hour {
			t.Fatalf("unscoped tracer discovery: %+v", query)
		}
	}
}

func TestQueryEventsRequiresTimeRangeAndPropagatesFilter(t *testing.T) {
	store := &fakeEventStore{searchDocuments: []*tracingstore.Document{{
		Document: types.Document{TracerID: "trace-1"},
		RecordID: "es-document-1",
	}}}
	router := testEventRouter(t, store)
	tools := &eventTools{
		stores:               router,
		maxDocumentsPerQuery: 20,
		maxRegions:           2,
		regionWorkers:        1,
	}
	arguments := map[string]any{
		constant.ArgumentTracerName: []any{"cpusys", "cpusys"},
		constant.ArgumentRegion:     []any{"cn"},
		constant.ArgumentHost:       []any{"node-1", "node-1"},
	}
	missing, err := tools.queryEvents(t.Context(), eventToolRequest(arguments))
	if err != nil || !missing.IsError || !strings.Contains(textResult(t, missing), "time_range is required") {
		t.Fatalf("queryEvents() without time range = %+v, %v", missing, err)
	}
	arguments[constant.ArgumentTimeRange] = "1h"
	result, err := tools.queryEvents(t.Context(), eventToolRequest(arguments))
	if err != nil || result.IsError {
		t.Fatalf("queryEvents() = %+v, %v", result, err)
	}
	text := textResult(t, result)
	if !strings.Contains(text, `"record_id": "es-document-1"`) ||
		!strings.Contains(text, `"region": "cn"`) ||
		!strings.Contains(text, `"tracer_id": "trace-1"`) {
		t.Fatalf("queryEvents() result = %s", text)
	}
	if len(store.searchFilters) != 1 {
		t.Fatalf("search filters = %+v", store.searchFilters)
	}
	filter := store.searchFilters[0]
	if filter.Region != "cn" || !reflect.DeepEqual(filter.Hostnames, []string{"node-1"}) ||
		!reflect.DeepEqual(filter.TracerNames, []string{"cpusys"}) || filter.EndTime.Sub(filter.StartTime) != time.Hour {
		t.Fatalf("search filter = %+v", filter)
	}
}

func TestQueryEventsPreservesPartialRegionDocuments(t *testing.T) {
	store := &fakeEventStore{
		counts:       map[string]int64{"small": 5, "large": 50},
		searchErrors: map[string]error{"large": errors.New("backend unavailable")},
	}
	router := testEventRouter(t, store)
	tools := &eventTools{
		stores: router,
		tracerCache: map[string]tracerCacheEntry{
			"cn": {timeRange: "1h", tracers: []string{"small", "large"}, fetched: time.Now()},
		},
		tracerCacheTTL:       time.Minute,
		maxDocumentsPerQuery: 20,
		maxRegions:           2,
		regionWorkers:        1,
	}
	metrics, err := NewMetrics(nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := metrics.Wrap(constant.ToolQueryEvents, tools.queryEvents)
	result, err := handler(t.Context(), eventToolRequest(map[string]any{
		constant.ArgumentTracerName: []any{"large", "small"},
		constant.ArgumentRegion:     []any{"cn"},
		constant.ArgumentHost:       []any{"node-1"},
		constant.ArgumentTimeRange:  "1h",
		constant.ArgumentSize:       20,
	}))
	if err != nil || result.IsError {
		t.Fatalf("queryEvents() = %+v, %v", result, err)
	}
	text := textResult(t, result)
	if !strings.Contains(text, `"events": [`) || !strings.Contains(text, "partial es query") {
		t.Fatalf("partial event result = %s", text)
	}
	if got := counterValue(t, metrics.calls.WithLabelValues(
		constant.ToolQueryEvents, constant.ToolOutcomePartialSuccess, constant.ToolErrorBackend,
	)); got != 1 {
		t.Fatalf("partial event calls = %v, want 1", got)
	}
	if got := counterValue(t, metrics.results.WithLabelValues(
		constant.ToolQueryEvents, constant.ToolResultPartial,
	)); got != 1 {
		t.Fatalf("partial event results = %v, want 1", got)
	}
}

func TestQueryEventsQueriesRequestedTracersOutsideDiscoveryCatalog(t *testing.T) {
	store := &fakeEventStore{searchDocuments: []*tracingstore.Document{{}}}
	router := testEventRouter(t, store)
	tools := &eventTools{
		stores: router,
		tracerCache: map[string]tracerCacheEntry{
			"cn": {timeRange: "1h", tracers: []string{"cpusys"}, fetched: time.Now()},
		},
		tracerCacheTTL:       time.Minute,
		maxDocumentsPerQuery: 20,
		maxRegions:           2,
		regionWorkers:        1,
	}
	result, err := tools.queryEvents(t.Context(), eventToolRequest(map[string]any{
		constant.ArgumentTracerName: []any{"cpusys", "missing"},
		constant.ArgumentRegion:     []any{"cn"},
		constant.ArgumentHost:       []any{"node-1"},
		constant.ArgumentTimeRange:  "1h",
	}))
	if err != nil || result.IsError || strings.Contains(textResult(t, result), "requested tracers are unavailable") || len(store.searchFilters) != 1 || !reflect.DeepEqual(store.searchFilters[0].TracerNames, []string{"cpusys", "missing"}) {
		t.Fatalf("queryEvents() = %+v, %v", result, err)
	}
}

func TestBoundedStringArrayRejectsInvalidValues(t *testing.T) {
	values, err := boundedStringArray(
		map[string]any{constant.ArgumentHost: []any{" node-1 ", "node-1"}},
		constant.ArgumentHost,
		false,
		2,
		10,
	)
	if err != nil || !reflect.DeepEqual(values, []string{"node-1"}) {
		t.Fatalf("boundedStringArray() = %v, %v", values, err)
	}
	for _, arguments := range []map[string]any{
		{constant.ArgumentHost: []any{"a", "b", "c"}},
		{constant.ArgumentHost: []any{""}},
		{constant.ArgumentHost: []any{"value-is-too-long"}},
		{constant.ArgumentHost: []any{"node-1", 42}},
	} {
		if _, err := boundedStringArray(arguments, constant.ArgumentHost, false, 2, 10); err == nil {
			t.Errorf("boundedStringArray(%v) error = nil", arguments)
		}
	}
}

func TestQueryProfilesAnalyzesAutomaticFrameData(t *testing.T) {
	store := &fakeEventStore{searchDocuments: []*tracingstore.Document{{
		Document: types.Document{
			Hostname:   "node-1",
			TracerName: "cpusys",
		},
		RecordID:      "es-profile-1",
		ProfileFrames: []flamegraph.FrameData{{Level: 0, Value: 100, Self: 0, Label: "all"}, {Level: 1, Value: 100, Self: 100, Label: "worker"}},
		TracerData: map[string]any{"flamedata": []map[string]any{
			{"level": 0, "value": 100, "self": 0, "label": "all"},
			{"level": 1, "value": 100, "self": 100, "label": "worker"},
		}},
	}}}
	router := testEventRouter(t, store)
	tools := &eventTools{
		stores:               router,
		maxDocumentsPerQuery: 20,
		maxRegions:           2,
		regionWorkers:        1,
	}
	arguments := map[string]any{
		constant.ArgumentTracerName: "cpusys",
		constant.ArgumentRegion:     []any{"cn"},
		constant.ArgumentHost:       []any{"node-1"},
	}
	missing, err := tools.queryProfiles(t.Context(), eventToolRequest(arguments))
	if err != nil || !missing.IsError || !strings.Contains(textResult(t, missing), "time_range is required") {
		t.Fatalf("queryProfiles() without time range = %+v, %v", missing, err)
	}
	arguments[constant.ArgumentTimeRange] = "1h"
	result, err := tools.queryProfiles(t.Context(), eventToolRequest(arguments))
	if err != nil || result.IsError {
		t.Fatalf("queryProfiles() = %+v, %v", result, err)
	}
	if len(store.searchFilters) != 1 || !store.searchFilters[0].ProfileOnly ||
		store.searchFilters[0].EndTime.Sub(store.searchFilters[0].StartTime) != time.Hour {
		t.Fatalf("profile search filters = %+v", store.searchFilters)
	}
	text := textResult(t, result)
	if !strings.Contains(text, `"payload_format": "frame_data"`) ||
		!strings.Contains(text, `"name": "worker"`) ||
		!strings.Contains(text, `"record_id": "es-profile-1"`) ||
		!strings.Contains(text, `"region": "cn"`) ||
		!strings.Contains(text, `"documents": 1`) {
		t.Fatalf("profile result = %s", text)
	}
}

func TestQueryProfilesCanAnalyzeOneEventBySourceRecordID(t *testing.T) {
	store := &fakeEventStore{searchDocuments: []*tracingstore.Document{
		{RecordID: "first", ProfileFrames: []flamegraph.FrameData{{Level: 0, Value: 20, Self: 20, Label: "userspace"}}},
		{RecordID: "second", ProfileFrames: []flamegraph.FrameData{{Level: 0, Value: 80, Self: 80, Label: "shrink_slab"}}},
	}}
	router := testEventRouter(t, store)
	tools := &eventTools{stores: router, maxDocumentsPerQuery: 100, maxRegions: 2, regionWorkers: 1}
	arguments := map[string]any{constant.ArgumentTracerName: "cpuidle", constant.ArgumentRegion: []any{"us01"}, constant.ArgumentContainerHost: []any{"pod"}, constant.ArgumentTimeRange: "1h", constant.ArgumentRecordID: "second"}
	result, err := tools.queryProfiles(t.Context(), eventToolRequest(arguments))
	if err != nil || result.IsError {
		t.Fatalf("single-source profile = %+v, %v", result, err)
	}
	var analysis profileAnalysisResult
	if err := json.Unmarshal([]byte(textResult(t, result)), &analysis); err != nil {
		t.Fatal(err)
	}
	if analysis.Summary.Documents != 1 || analysis.Summary.TotalSamples != 80 || len(analysis.TopFunctions) != 1 || analysis.TopFunctions[0].Name != "shrink_slab" || len(analysis.Sources) != 1 || analysis.Sources[0].Source.RecordID != "second" || len(store.searchFilters) != 1 || store.searchFilters[0].RecordID != "second" || store.searchFilters[0].Limit != 1 {
		t.Fatalf("single-source profile included other events: %+v filters=%+v", analysis, store.searchFilters)
	}
	arguments[constant.ArgumentRecordID] = "missing"
	missing, err := tools.queryProfiles(t.Context(), eventToolRequest(arguments))
	if err != nil || !missing.IsError || !strings.Contains(textResult(t, missing), "no analyzable profile matches record_id") {
		t.Fatalf("missing record ID silently accepted: %+v, %v", missing, err)
	}
	arguments[constant.ArgumentRecordID] = "second"
	arguments[constant.ArgumentRegion] = []any{"us01", "cn"}
	multiple, err := tools.queryProfiles(t.Context(), eventToolRequest(arguments))
	if err != nil || !multiple.IsError || !strings.Contains(textResult(t, multiple), "exactly one region") {
		t.Fatalf("multi-region record ID accepted: %+v, %v", multiple, err)
	}
}

func TestEventToolsAcceptForeignReaderAndNormalizedProfileFrames(t *testing.T) {
	document := &tracingstore.Document{
		RecordID: "row-7", TracerData: map[string]any{"native_payload": true},
		Source:        &tracingstore.SourceRef{Backend: "external", RecordID: "row-7", Region: "stale", Locator: map[string]string{"stream": "cpu"}},
		ProfileFrames: []flamegraph.FrameData{{Level: 0, Value: 10, Self: 10, Label: "worker"}},
	}
	store := &fakeEventStore{backendType: "external", searchDocuments: []*tracingstore.Document{document}}
	router := testEventRouter(t, store)
	tools := &eventTools{
		stores: router, maxDocumentsPerQuery: 20, maxRegions: 1, regionWorkers: 1,
		tracerCacheTTL: time.Minute,
		tracerCache:    map[string]tracerCacheEntry{"cn": {timeRange: "1h", tracers: []string{"cpusys"}, profiler: map[string]bool{"cpusys": true}, fetched: time.Now()}},
	}
	events, err := tools.queryEvents(t.Context(), eventToolRequest(map[string]any{
		constant.ArgumentTracerName: []any{"cpusys"}, constant.ArgumentRegion: []any{"cn"},
		constant.ArgumentHost: []any{"node-1"}, constant.ArgumentTimeRange: "1h",
	}))
	if err != nil || events.IsError {
		t.Fatalf("foreign event query=%+v, %v", events, err)
	}
	text := textResult(t, events)
	if strings.Contains(text, `"_id"`) || !strings.Contains(text, `"backend": "external"`) || !strings.Contains(text, `"record_id": "row-7"`) || !strings.Contains(text, `"query_status"`) {
		t.Fatalf("foreign source envelope=%s", text)
	}
	profiles, err := tools.queryProfiles(t.Context(), eventToolRequest(map[string]any{
		constant.ArgumentTracerName: "cpusys", constant.ArgumentRegion: []any{"cn"},
		constant.ArgumentHost: []any{"node-1"}, constant.ArgumentTimeRange: "1h",
	}))
	if err != nil || profiles.IsError {
		t.Fatalf("foreign profile query=%+v, %v", profiles, err)
	}
	text = textResult(t, profiles)
	if strings.Contains(text, `"_id"`) || !strings.Contains(text, `"name": "worker"`) || !strings.Contains(text, `"backend": "external"`) {
		t.Fatalf("foreign normalized profile=%s", text)
	}
	if document.Source.Region != "stale" {
		t.Fatal("tool mutated source locator")
	}
}

func TestQueryProfilesMarksSkewedRegionTruncation(t *testing.T) {
	document := &tracingstore.Document{ProfileFrames: []flamegraph.FrameData{
		{Level: 0, Value: 10, Self: 10, Label: "worker"},
	}}
	store := &fakeEventStore{searchDocuments: []*tracingstore.Document{document, document}}
	empty := &fakeEventStore{searchDocuments: []*tracingstore.Document{}}
	router, err := newRegionRouter[tracingstore.Reader](store, []regionValue[tracingstore.Reader]{{name: "empty", value: empty}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		maximum, documents int
		truncated          bool
	}{
		{maximum: 2, documents: 1, truncated: true},
		{maximum: 4, documents: 2, truncated: false},
	} {
		tools := &eventTools{
			stores: router, maxDocumentsPerQuery: test.maximum, maxRegions: 2, regionWorkers: 1,
			tracerCacheTTL: time.Minute,
			tracerCache: map[string]tracerCacheEntry{
				"cn":    {timeRange: "1h", profiler: map[string]bool{"cpusys": true}, fetched: time.Now()},
				"empty": {timeRange: "1h", profiler: map[string]bool{"cpusys": true}, fetched: time.Now()},
			},
		}
		metrics, err := NewMetrics(nil)
		if err != nil {
			t.Fatal(err)
		}
		result, err := metrics.Wrap(constant.ToolQueryProfiles, tools.queryProfiles)(t.Context(), eventToolRequest(map[string]any{
			constant.ArgumentTracerName: "cpusys", constant.ArgumentRegion: []any{"cn", "empty"},
			constant.ArgumentHost: []any{"node-1"}, constant.ArgumentTimeRange: "1h",
		}))
		if err != nil || result.IsError {
			t.Fatalf("profile query: %+v, %v", result, err)
		}
		var analysis profileAnalysisResult
		if err := json.Unmarshal([]byte(textResult(t, result)), &analysis); err != nil {
			t.Fatal(err)
		}
		if analysis.Summary.Truncated != test.truncated || analysis.Summary.Documents != test.documents || analysis.Summary.TotalSamples != int64(test.documents*10) {
			t.Fatalf("incomplete analysis not identified: %+v", analysis.Summary)
		}
		outcome := constant.ToolOutcomeSuccess
		if test.truncated {
			outcome = constant.ToolOutcomePartialSuccess
		}
		if got := counterValue(t, metrics.calls.WithLabelValues(constant.ToolQueryProfiles, outcome, constant.ToolErrorNone)); got != 1 {
			t.Fatalf("%s calls = %v", outcome, got)
		}
	}
}

func TestTracerCacheEvictsOldestAndExpiredRegions(t *testing.T) {
	store := &fakeEventStore{}
	router := testEventRouter(t, store)
	tools := &eventTools{stores: router, tracerCache: make(map[string]tracerCacheEntry), tracerCacheTTL: time.Hour}
	for index := range constant.MaxTracerCacheEntries {
		tools.tracerCache[fmt.Sprint(index)] = tracerCacheEntry{fetched: time.Now()}
	}
	tools.tracerCache["0"] = tracerCacheEntry{fetched: time.Now().Add(-time.Minute)}
	if _, err := tools.discoverRegionTracers(t.Context(), "new", "1h", time.Now().Add(-time.Hour), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(tools.tracerCache) != constant.MaxTracerCacheEntries {
		t.Fatalf("cache size = %d", len(tools.tracerCache))
	}
	if _, exists := tools.tracerCache["0"]; exists {
		t.Fatal("oldest entry retained")
	}
	tools.tracerCache["expired"] = tracerCacheEntry{fetched: time.Now().Add(-2 * time.Hour)}
	if _, err := tools.discoverRegionTracers(t.Context(), "another", "1h", time.Now().Add(-time.Hour), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, exists := tools.tracerCache["expired"]; exists {
		t.Fatal("expired entry retained")
	}
	if len(tools.tracerCache) != constant.MaxTracerCacheEntries {
		t.Fatalf("cache size = %d", len(tools.tracerCache))
	}
}

func TestAnalyzeProfilesRejectsUnsupportedPayload(t *testing.T) {
	_, err := (&eventTools{}).analyzeProfilerDocuments(
		t.Context(),
		[]*tracingstore.Document{{TracerData: map[string]any{"flamedata": map[string]any{"profile": true}}}},
		[]string{"cn"},
		"cpusys",
		map[string][]string{constant.ArgumentHost: {"node-1"}},
		"start/end",
		"self_time",
		10,
		5,
		12,
	)
	if err == nil || !strings.Contains(err.Error(), "no supported tracer_data.flamedata") {
		t.Fatalf("analyzeProfilerDocuments() error = %v", err)
	}
}

func TestQueryProfilesRecordsUnsupportedPayload(t *testing.T) {
	store := &fakeEventStore{searchDocuments: []*tracingstore.Document{{
		TracerData: map[string]any{"flamedata": map[string]any{"profile": true}},
	}}}
	router := testEventRouter(t, store)
	tools := &eventTools{
		stores: router,
		tracerCache: map[string]tracerCacheEntry{
			"cn": {timeRange: "1h", tracers: []string{"cpusys"}, profiler: map[string]bool{"cpusys": true}, fetched: time.Now()},
		},
		tracerCacheTTL:       time.Minute,
		maxDocumentsPerQuery: 20,
		maxRegions:           2,
		regionWorkers:        1,
	}
	metrics, err := NewMetrics(nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := metrics.Wrap(constant.ToolQueryProfiles, tools.queryProfiles)
	result, err := handler(t.Context(), eventToolRequest(map[string]any{
		constant.ArgumentTracerName: "cpusys",
		constant.ArgumentRegion:     []any{"cn"},
		constant.ArgumentHost:       []any{"node-1"},
		constant.ArgumentTimeRange:  "1h",
	}))
	if err != nil || !result.IsError {
		t.Fatalf("queryProfiles() = %+v, %v", result, err)
	}
	if got := counterValue(t, metrics.calls.WithLabelValues(
		constant.ToolQueryProfiles, constant.ToolOutcomeError, constant.ToolErrorUnsupportedPayload,
	)); got != 1 {
		t.Fatalf("unsupported profile calls = %v, want 1", got)
	}
	if got := counterValue(t, metrics.results.WithLabelValues(
		constant.ToolQueryProfiles, constant.ToolResultUnsupportedPayload,
	)); got != 1 {
		t.Fatalf("unsupported profile results = %v, want 1", got)
	}
	if got := counterValue(t, metrics.profileDocuments.WithLabelValues("unsupported")); got != 1 {
		t.Fatalf("unsupported profile documents = %v, want 1", got)
	}
}

func TestEventStoreContractIsReadOnly(t *testing.T) {
	contract := reflect.TypeOf((*tracingstore.Reader)(nil)).Elem()
	for _, forbidden := range []string{"GetRecords", "Save", "Delete", "DeleteByQuery"} {
		if _, exists := contract.MethodByName(forbidden); exists {
			t.Errorf("EventStore unexpectedly exposes %s", forbidden)
		}
	}
}

func TestDiscoverTracersRoutesAndCachesPerRegion(t *testing.T) {
	fallback := &fakeEventStore{}
	regional := &fakeEventStore{}
	router, err := newRegionRouter[tracingstore.Reader](fallback, []regionValue[tracingstore.Reader]{
		{name: "^bj-", value: regional},
	})
	if err != nil {
		t.Fatal(err)
	}
	tools := &eventTools{
		stores:         router,
		tracerCache:    make(map[string]tracerCacheEntry),
		tracerCacheTTL: time.Minute,
	}
	request := eventToolRequest(map[string]any{constant.ArgumentRegion: []any{"bj-1"}, constant.ArgumentTimeRange: "1h"})

	first, err := tools.discoverTracers(t.Context(), request)
	if err != nil || first.IsError {
		t.Fatalf("discoverTracers() = %+v, %v", first, err)
	}
	second, err := tools.discoverTracers(t.Context(), request)
	if err != nil || second.IsError {
		t.Fatalf("cached discoverTracers() = %+v, %v", second, err)
	}
	if regional.tracerNamesCalls != 2 || fallback.tracerNamesCalls != 0 {
		t.Fatalf("TracerNames calls regional/fallback = %d/%d, want 2/0", regional.tracerNamesCalls, fallback.tracerNamesCalls)
	}
	changed := eventToolRequest(map[string]any{constant.ArgumentRegion: []any{"bj-1"}, constant.ArgumentTimeRange: "2h"})
	if result, err := tools.discoverTracers(t.Context(), changed); err != nil || result.IsError {
		t.Fatalf("changed-range discovery: %+v, %v", result, err)
	}
	if regional.tracerNamesCalls != 4 || regional.tracerFilters[2].EndTime.Sub(regional.tracerFilters[2].StartTime) != 2*time.Hour {
		t.Fatalf("different time range reused cached catalog: %+v", regional.tracerFilters)
	}
	if !strings.Contains(textResult(t, first), `"region": "bj-1"`) ||
		!strings.Contains(textResult(t, first), `"tracer_name": "cpusys"`) ||
		!strings.Contains(textResult(t, first), `"has_profiler": true`) ||
		textResult(t, second) != textResult(t, first) {
		t.Fatalf("results = %q / %q", textResult(t, first), textResult(t, second))
	}
}

func TestSearchEventsGreedyBatchesReturnPartialResults(t *testing.T) {
	store := &fakeEventStore{
		counts: map[string]int64{"small": 5, "medium": 10, "large": 50},
		searchErrors: map[string]error{
			"large": errors.New("backend unavailable"),
		},
	}
	tools := &eventTools{}
	documents, err := tools.searchEvents(
		t.Context(),
		store,
		&tracingstore.Query{Region: "cn"},
		[]string{"large", "small", "medium"},
		20,
	)
	if len(documents.Documents) != 1 {
		t.Fatalf("documents = %d, want 1", len(documents.Documents))
	}
	var partial *partialQueryError
	if !errors.As(err, &partial) {
		t.Fatalf("error = %v, want partialQueryError", err)
	}
	if !reflect.DeepEqual(store.searches, [][]string{{"small", "medium"}, {"large"}}) {
		t.Fatalf("search batches = %v", store.searches)
	}
	if !reflect.DeepEqual(partial.FailedTracers, []string{"large"}) || partial.ReturnedRecords != 1 {
		t.Fatalf("partial error = %+v", partial)
	}
	if !errors.Is(err, store.searchErrors["large"]) || !strings.Contains(err.Error(), "backend unavailable") || len(partial.Failures) != 1 || !reflect.DeepEqual(partial.Failures[0].Tracers, []string{"large"}) {
		t.Fatalf("batch query cause lost: %+v", partial)
	}
}

func TestCountFallbackPreservesFailureAndUnknownTotal(t *testing.T) {
	countErr := errors.New("Count permission denied")
	searchErr := errors.New("search unavailable")
	for _, failSearch := range []bool{false, true} {
		store := &fakeEventStore{countError: countErr}
		if failSearch {
			store.searchErrors = map[string]error{stringsKey([]string{"a", "b"}): searchErr}
		}
		result, err := (&eventTools{}).searchEvents(t.Context(), store, &tracingstore.Query{Region: "us01"}, []string{"a", "b"}, 10)
		if len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0], countErr.Error()) || !strings.Contains(result.Warnings[0], "fell back") || result.TotalCount != nil || result.CountExact {
			t.Fatalf("Count fallback hid the error: %+v, %v", result, err)
		}
		if failSearch {
			if !errors.Is(err, countErr) || !errors.Is(err, searchErr) {
				t.Fatalf("Count or Search cause lost: %v", err)
			}
		} else if err != nil || len(result.Documents) != 1 {
			t.Fatalf("successful fallback lost data: %+v, %v", result, err)
		}
	}
}

func TestProfileFailureRetainsBackendErrorsAndPayloadWarnings(t *testing.T) {
	broken := &fakeEventStore{searchErrors: map[string]error{"cpusys": errors.New("backend read denied")}}
	unsupported := &fakeEventStore{searchDocuments: []*tracingstore.Document{{}}, searchWarnings: []string{"profile payload could not be analyzed: invalid pprof JSON"}}
	router, err := newRegionRouter[tracingstore.Reader](broken, []regionValue[tracingstore.Reader]{{name: "cn", value: unsupported}})
	if err != nil {
		t.Fatal(err)
	}
	tools := &eventTools{stores: router, maxRegions: 2, maxQueryTimeRange: time.Hour, maxDocumentsPerQuery: 10}
	var recorder *Metrics
	result, err := recorder.Wrap(constant.ToolQueryProfiles, tools.queryProfiles)(t.Context(), eventToolRequest(map[string]any{
		"region": []any{"cn", "us01"}, "tracer_name": "cpusys", "host": []any{"node-1"}, "time_range": "1m",
	}))
	if err != nil || !result.IsError {
		t.Fatalf("expected profile analysis failure: %+v, %v", result, err)
	}
	var payload toolErrorEnvelope
	if err := json.Unmarshal([]byte(textResult(t, result)), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Errors) != 1 || len(payload.QueryStatus) != 2 || !strings.Contains(textResult(t, result), "backend read denied") || !strings.Contains(textResult(t, result), "invalid pprof JSON") {
		t.Fatalf("profile failure lost diagnostic context: %s", textResult(t, result))
	}
}

func TestSearchEventsUsesTracerCountAggregation(t *testing.T) {
	store := &countingFakeEventStore{
		fakeEventStore: &fakeEventStore{
			counts: map[string]int64{"alpha": 30, "beta": 30, "missing": 30},
		},
		tracerCounts: []tracingstore.TracerCount{
			{Name: "beta", Count: 10},
			{Name: "alpha", Count: 10},
		},
	}
	documents, err := (&eventTools{}).searchEvents(
		t.Context(),
		store,
		&tracingstore.Query{Region: "cn"},
		[]string{"beta", "missing", "alpha"},
		20,
	)
	if err != nil || len(documents.Documents) != 1 {
		t.Fatalf("searchEvents() = %d documents, %v", len(documents.Documents), err)
	}
	if store.tracerCountsCalls != 1 || !reflect.DeepEqual(store.countCalls, [][]string{{"beta", "missing", "alpha"}}) {
		t.Fatalf("aggregation/count calls = %d/%v", store.tracerCountsCalls, store.countCalls)
	}
	if store.tracerCountFilter.Limit != 3 || store.tracerCountFilter.Region != "cn" {
		t.Fatalf("aggregation filter = %+v", store.tracerCountFilter)
	}
	if !reflect.DeepEqual(store.searches, [][]string{{"missing", "alpha", "beta"}}) {
		t.Fatalf("search batches = %v", store.searches)
	}
}

func TestSearchEventsFallsBackForUnsupportedTracerCountAggregation(t *testing.T) {
	store := &countingFakeEventStore{
		fakeEventStore:  &fakeEventStore{counts: map[string]int64{"a": 5, "b": 30}},
		tracerCountsErr: tracingstore.ErrUnsupportedOp,
	}
	_, err := (&eventTools{}).searchEvents(
		t.Context(),
		store,
		&tracingstore.Query{},
		[]string{"b", "a"},
		20,
	)
	if err != nil {
		t.Fatalf("searchEvents() error = %v", err)
	}
	if store.tracerCountsCalls != 1 || !reflect.DeepEqual(store.countCalls, [][]string{{"b", "a"}, {"b"}, {"a"}}) {
		t.Fatalf("aggregation/count calls = %d/%v", store.tracerCountsCalls, store.countCalls)
	}
	if !reflect.DeepEqual(store.searches, [][]string{{"a"}, {"b"}}) {
		t.Fatalf("search batches = %v", store.searches)
	}
}

func TestSearchEventsReturnsTracerCountBackendFailure(t *testing.T) {
	store := &countingFakeEventStore{
		fakeEventStore:  &fakeEventStore{counts: map[string]int64{"a": 30, "b": 30}},
		tracerCountsErr: errors.New("backend unavailable"),
	}
	_, err := (&eventTools{}).searchEvents(
		t.Context(), store, &tracingstore.Query{}, []string{"a", "b"}, 20,
	)
	if err == nil || !strings.Contains(err.Error(), "backend unavailable") {
		t.Fatalf("searchEvents() error = %v", err)
	}
	if store.tracerCountsCalls != 1 || !reflect.DeepEqual(store.countCalls, [][]string{{"a", "b"}}) {
		t.Fatalf("aggregation/count calls = %d/%v", store.tracerCountsCalls, store.countCalls)
	}
}

func TestSearchEventsBoundsCombinedBatchResults(t *testing.T) {
	store := &sizedFakeEventStore{fakeEventStore: &fakeEventStore{
		counts: map[string]int64{"a": 3, "b": 3},
	}}
	documents, err := (&eventTools{}).searchEvents(
		t.Context(),
		store,
		&tracingstore.Query{},
		[]string{"a", "b"},
		4,
	)
	if err != nil {
		t.Fatalf("searchEvents() error = %v", err)
	}
	if len(documents.Documents) != 4 {
		t.Fatalf("searchEvents() returned %d documents, want 4", len(documents.Documents))
	}
	if !reflect.DeepEqual(store.searchLimits, []int{4, 1}) {
		t.Fatalf("search limits = %v, want [4 1]", store.searchLimits)
	}
}

func TestDistributeLimitRejectsSkippedRegions(t *testing.T) {
	if _, err := distributeLimit(1, []string{"cn", "us"}); err == nil {
		t.Fatal("distributeLimit() error = nil")
	}
	limits, err := distributeLimit(3, []string{"cn", "us"})
	if err != nil || !reflect.DeepEqual(limits, map[string]int{"cn": 2, "us": 1}) {
		t.Fatalf("distributeLimit() = %v, %v", limits, err)
	}
}

func TestAggregateFramesPreservesHotspotResponseSemantics(t *testing.T) {
	stats := make(map[string]*frameFunctionStat)
	var total int64
	analyzedFrames := 0
	aggregatedPaths := 0
	if err := aggregateFrames(t.Context(), []flamegraph.FrameData{
		{Level: 0, Value: 100, Label: "root"},
		{Level: 1, Value: 70, Self: 20, Label: "worker"},
		{Level: 2, Value: 50, Self: 50, Label: "leaf"},
		{Level: 1, Value: 30, Self: 30, Label: "syscall"},
	}, stats, &total, &analyzedFrames, &aggregatedPaths); err != nil {
		t.Fatal(err)
	}
	result, err := buildProfileAnalysis(
		t.Context(),
		stats,
		total,
		"cpusys",
		"start/end",
		"self_time",
		3,
		2,
		2,
	)
	if err != nil {
		t.Fatal(err)
	}

	if result.Summary.TotalSamples != 100 || result.Summary.UniqueFunctions != 4 {
		t.Fatalf("summary = %+v", result.Summary)
	}
	if result.TopFunctions[0].Name != "leaf" || result.TopFunctions[0].SelfPercent != 50 ||
		result.TopFunctions[0].TotalPercent != 50 || result.TopFunctions[0].SampleCount != 50 {
		t.Fatalf("top function = %+v", result.TopFunctions[0])
	}
	if got := result.TopFunctions[0].CallPaths; len(got) != 1 || !reflect.DeepEqual(got[0].Path, []string{"leaf", "worker"}) || got[0].SelfSamples != 50 || got[0].TotalSamples != 50 {
		t.Fatalf("leaf call paths = %+v", got)
	}
	if result.Summary.Coverage != 100 {
		t.Fatalf("coverage = %v, want 100", result.Summary.Coverage)
	}
}

func TestProfileTotalTimeIncludesZeroSelfCallerPaths(t *testing.T) {
	for _, self := range []int64{0, 5} {
		stats := make(map[string]*frameFunctionStat)
		var total int64
		frames, paths := 0, 0
		if err := aggregateFrames(t.Context(), []flamegraph.FrameData{
			{Level: 0, Value: 100, Label: "root"},
			{Level: 1, Value: 90, Label: "caller-a"},
			{Level: 2, Value: 90, Label: "worker"},
			{Level: 3, Value: 90, Self: 90, Label: "leaf"},
			{Level: 1, Value: 10, Label: "caller-b"},
			{Level: 2, Value: 10, Self: self, Label: "worker"},
			{Level: 3, Value: 10 - self, Self: 10 - self, Label: "leaf"},
		}, stats, &total, &frames, &paths); err != nil {
			t.Fatal(err)
		}
		for _, analysisType := range []string{constant.ProfileAnalysisTotalTime, constant.ProfileAnalysisSelfTime} {
			result, err := buildProfileAnalysis(t.Context(), stats, total, "cpusys", "1h", analysisType, 10, 5, 12)
			if err != nil {
				t.Fatal(err)
			}
			var worker *hotspotFunction
			for index := range result.TopFunctions {
				if result.TopFunctions[index].Name == "worker" {
					worker = &result.TopFunctions[index]
				}
			}
			if worker == nil || worker.TotalPercent != 100 || worker.SelfPercent != float64(self) || worker.SampleCount != self {
				t.Fatalf("self=%d %s hotspot=%+v", self, analysisType, worker)
			}
			if analysisType == constant.ProfileAnalysisTotalTime {
				if len(worker.CallPaths) != 2 || !reflect.DeepEqual(worker.CallPaths[0].Path, []string{"worker", "caller-a", "root"}) || worker.CallPaths[0].TotalSamples != 90 || worker.CallPaths[0].SelfSamples != 0 || worker.CallPaths[0].Percentage != 90 || worker.CallPaths[1].TotalSamples != 10 || worker.CallPaths[1].SelfSamples != self {
					t.Fatalf("total-time paths=%+v", worker.CallPaths)
				}
			} else if self == 0 {
				if len(worker.CallPaths) != 0 {
					t.Fatalf("zero-self paths included in self-time analysis: %+v", worker.CallPaths)
				}
			} else if len(worker.CallPaths) != 1 || !reflect.DeepEqual(worker.CallPaths[0].Path, []string{"worker", "caller-b", "root"}) || worker.CallPaths[0].SelfSamples != self {
				t.Fatalf("self-time paths changed: %+v", worker.CallPaths)
			}
		}
		merged, err := buildProfileAnalysis(t.Context(), stats, total, "cpusys", "1h", constant.ProfileAnalysisTotalTime, 10, 5, 1)
		if err != nil {
			t.Fatal(err)
		}
		for _, function := range merged.TopFunctions {
			if function.Name == "worker" && (len(function.CallPaths) != 1 || function.CallPaths[0].TotalSamples != 100 || function.CallPaths[0].SelfSamples != self) {
				t.Fatalf("inclusive paths not merged at requested depth: %+v", function.CallPaths)
			}
		}
	}
}

func TestProfileSharedPathKeepsSelfAndInclusiveSamplesSeparate(t *testing.T) {
	stats := make(map[string]*frameFunctionStat)
	var total int64
	frames, paths := 0, 0
	for _, samples := range []struct{ value, self int64 }{{100, 0}, {10, 5}} {
		if err := aggregateFrames(t.Context(), []flamegraph.FrameData{
			{Level: 0, Value: samples.value, Label: "root"},
			{Level: 1, Value: samples.value, Self: samples.self, Label: "worker"},
			{Level: 2, Value: samples.value - samples.self, Self: samples.value - samples.self, Label: "leaf"},
		}, stats, &total, &frames, &paths); err != nil {
			t.Fatal(err)
		}
	}
	for _, analysisType := range []string{constant.ProfileAnalysisSelfTime, constant.ProfileAnalysisTotalTime} {
		result, err := buildProfileAnalysis(t.Context(), stats, total, "cpusys", "1h", analysisType, 10, 5, 12)
		if err != nil {
			t.Fatal(err)
		}
		wantTotal := int64(110)
		if analysisType == constant.ProfileAnalysisSelfTime {
			wantTotal = 10
		}
		found := false
		for _, function := range result.TopFunctions {
			if function.Name != "worker" {
				continue
			}
			found = true
			if len(function.CallPaths) != 1 || function.CallPaths[0].SelfSamples != 5 || function.CallPaths[0].TotalSamples != wantTotal {
				t.Fatalf("%s shared path=%+v", analysisType, function.CallPaths)
			}
		}
		if !found {
			t.Fatal("worker hotspot missing")
		}
	}
}

func TestProfileAnalysisReportsPprofUnitsAndRejectsIncompatibleWeights(t *testing.T) {
	document := &tracingstore.Document{ProfileFrames: []flamegraph.FrameData{{Level: 0, Label: "root", Value: 100}, {Level: 1, Label: "worker", Value: 100, Self: 100}}, ProfileSampleType: "cpu", ProfileSampleUnit: "nanoseconds"}
	tools := &eventTools{}
	result, err := tools.analyzeProfilerDocuments(t.Context(), []*tracingstore.Document{document}, []string{"cn"}, "cpuidle", nil, "1h", "self_time", 5, 5, 12)
	if err != nil || result.Summary.SampleType != "cpu" || result.Summary.SampleUnit != "nanoseconds" || result.Summary.TotalSamples != 100 || result.TopFunctions[0].SelfPercent != 100 {
		t.Fatalf("pprof units/weights=%+v, %v", result, err)
	}
	other := *document
	other.ProfileSampleUnit = "bytes"
	if _, err := tools.analyzeProfilerDocuments(t.Context(), []*tracingstore.Document{document, &other}, []string{"cn"}, "cpuidle", nil, "1h", "self_time", 5, 5, 12); err == nil || !strings.Contains(err.Error(), "incompatible sample types/units") {
		t.Fatalf("incompatible units were combined: %v", err)
	}
}

func TestAggregateFramesBoundsStoredCallPathDepth(t *testing.T) {
	frames := make([]flamegraph.FrameData, constant.MaxProfileStackDepth+10)
	for index := range frames {
		frames[index] = flamegraph.FrameData{
			Level: int64(index),
			Value: 1,
			Label: strings.Repeat("x", index+1),
		}
	}
	frames[len(frames)-1].Self = 1
	stats := make(map[string]*frameFunctionStat)
	var total int64
	analyzedFrames := 0
	aggregatedPaths := 0
	if err := aggregateFrames(
		t.Context(), frames, stats, &total, &analyzedFrames, &aggregatedPaths,
	); err != nil {
		t.Fatal(err)
	}
	leaf := stats[frames[len(frames)-1].Label]
	for _, path := range leaf.leafPaths {
		if len(path.path) != constant.MaxProfileStackDepth {
			t.Fatalf("stored call path depth = %d, want %d", len(path.path), constant.MaxProfileStackDepth)
		}
	}
}

func TestBuildProfileAnalysisMergesPathsAtRequestedDepth(t *testing.T) {
	stats := map[string]*frameFunctionStat{
		"leaf": {
			name:        "leaf",
			selfValue:   7,
			totalValue:  10,
			sampleCount: 7,
			leafPaths: map[string]*framePathStat{
				"a": {path: []string{"leaf", "caller-a"}, selfValue: 2, totalValue: 3, selfPathTotal: 3},
				"b": {path: []string{"leaf", "caller-b"}, selfValue: 5, totalValue: 7, selfPathTotal: 7},
			},
		},
	}
	result, err := buildProfileAnalysis(
		t.Context(), stats, 10, "cpusys", "1h", "self_time", 1, 5, 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	paths := result.TopFunctions[0].CallPaths
	if len(paths) != 1 || !reflect.DeepEqual(paths[0].Path, []string{"leaf"}) ||
		paths[0].SelfSamples != 7 || paths[0].TotalSamples != 10 {
		t.Fatalf("merged call paths = %+v", paths)
	}
}

func testEventRouter(t *testing.T, store tracingstore.Reader) *regionRouter[tracingstore.Reader] {
	t.Helper()
	router, err := newRegionRouter[tracingstore.Reader](store, nil)
	if err != nil {
		t.Fatal(err)
	}
	return router
}

func eventToolRequest(arguments map[string]any) mcpgo.CallToolRequest {
	return mcpgo.CallToolRequest{Params: mcpgo.CallToolParams{Arguments: arguments}}
}

func textResult(t *testing.T, result *mcpgo.CallToolResult) string {
	t.Helper()
	return result.Content[0].(mcpgo.TextContent).Text
}

func stringsKey(values []string) string {
	if len(values) == 0 {
		return ""
	}
	key := values[0]
	for _, value := range values[1:] {
		key += "," + value
	}
	return key
}

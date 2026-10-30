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

package storage

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestAggregateEventsUsesBoundedIndexedComposite(t *testing.T) {
	reader := newTestReader(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(body)
		for _, required := range []string{`"size":0`, `"timeout":"`, `"composite"`, `"size":2`, `"hostname.keyword"`, `"region.keyword":["us"]`, `"missing_bucket":true`, `"min"`, `"max"`} {
			if !strings.Contains(string(encoded), required) {
				t.Errorf("missing %s in %s", required, encoded)
			}
		}
		if r.URL.Path != "/events/_search" || strings.Contains(string(encoded), `"_source"`) || strings.Contains(string(encoded), `"top_hits"`) {
			t.Errorf("aggregation bypasses selector or reads documents: %s %s", r.URL.Path, encoded)
		}
		_, _ = w.Write([]byte(`{"aggregations":{"groups":{"buckets":[{"key":{"hostname":"node-a"},"doc_count":7,"first_seen":{"value":100000},"last_seen":{"value":200000}},{"key":{"hostname":null},"doc_count":2,"first_seen":{"value":100000},"last_seen":{"value":100000}}]}}}`))
	}, nil)
	query := &event.AggregationQuery{Filter: &event.Query{Region: "us", StartTime: time.Unix(100, 0), EndTime: time.Unix(200, 0)}, GroupBy: []string{"hostname"}, Limit: 1}
	result, err := reader.AggregateEvents(t.Context(), query)
	if err != nil || !result.CountExact || !result.Truncated || len(result.Buckets) != 1 || result.Buckets[0].Count != 7 || *result.Buckets[0].Key["hostname"] != "node-a" || !result.Buckets[0].FirstSeen.Equal(time.Unix(100, 0)) {
		t.Fatalf("aggregate=%+v, %v", result, err)
	}
	reader.fields.sourceFields = map[string]bool{"hostname": true}
	if _, err := reader.AggregateEvents(t.Context(), query); !errors.Is(err, event.ErrUnsupportedOp) {
		t.Fatalf("source grouping accepted: %v", err)
	}
	reader.fields.sourceFields = nil
	reader.fields.exact["hostname"] = append(reader.fields.exact["hostname"], exactField{Name: "hostname"})
	if _, err := reader.AggregateEvents(t.Context(), query); !errors.Is(err, event.ErrUnsupportedOp) {
		t.Fatalf("mixed grouping accepted: %v", err)
	}
	reader.fields.sourceTime = true
	if _, err := reader.AggregateEvents(t.Context(), query); !errors.Is(err, event.ErrUnsupportedOp) {
		t.Fatalf("source time scan accepted: %v", err)
	}
}

func TestAggregateEventsRejectsInvalidQueriesAndResponses(t *testing.T) {
	query := &event.AggregationQuery{Filter: &event.Query{Region: "us", StartTime: time.Unix(100, 0), EndTime: time.Unix(200, 0)}, GroupBy: []string{"hostname"}, Limit: 1}
	boundary := *query.Filter
	boundary.EndTime = boundary.StartTime.Add(event.MaxAggregationTimeRange)
	if err := validateAggregation(&event.AggregationQuery{Filter: &boundary, GroupBy: event.AggregationFields(), Limit: event.MaxAggregationBuckets}); err != nil {
		t.Fatalf("seven-day aggregation at bucket limit rejected: %v", err)
	}
	for _, mutate := range []func(*event.AggregationQuery){
		func(q *event.AggregationQuery) { q.Filter = nil },
		func(q *event.AggregationQuery) { q.Limit = event.MaxAggregationBuckets + 1 },
		func(q *event.AggregationQuery) { q.GroupBy = []string{"tracer_data.secret"} },
		func(q *event.AggregationQuery) { q.GroupBy = []string{"hostname", "hostname"} },
		func(q *event.AggregationQuery) {
			q.Filter.EndTime = q.Filter.StartTime.Add(event.MaxAggregationTimeRange + time.Second)
		},
	} {
		copyQuery, filter := *query, *query.Filter
		copyQuery.Filter = &filter
		mutate(&copyQuery)
		if err := validateAggregation(&copyQuery); !errors.Is(err, event.ErrInvalidQuery) {
			t.Errorf("invalid aggregation accepted: %v", err)
		}
	}
	for _, raw := range []string{`{}`, `{"buckets":null}`, `{"buckets":[{"key":{"hostname":"a"},"doc_count":-1}]}`, `{"buckets":[{"key":{"hostname":4},"doc_count":1}]}`, `{"buckets":[{"key":{"hostname":"a"},"doc_count":1,"first_seen":{"value":3},"last_seen":{"value":2}}]}`} {
		if _, err := decodeAggregation(json.RawMessage(raw), query); !errors.Is(err, event.ErrDecodeFailed) {
			t.Errorf("invalid response accepted: %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"timed_out":true}`, `{"_shards":{"failed":1}}`, `{"terminated_early":true}`} {
		reader := newTestReader(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(raw)) }, nil)
		if _, err := reader.AggregateEvents(t.Context(), query); !errors.Is(err, event.ErrIncompleteData) {
			t.Errorf("incomplete response accepted: %v", err)
		}
	}
	reader := newTestReader(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"aggregations":{"composite#groups":{"buckets":[{"key":{"hostname":null},"doc_count":1,"first_seen":{"value":100000},"last_seen":{"value":200000}}]}}}`))
	}, nil)
	result, err := reader.AggregateEvents(t.Context(), query)
	if err != nil || len(result.Buckets) != 1 || result.Buckets[0].Key["hostname"] != nil {
		t.Fatalf("missing grouping value lost: %+v %v", result, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := reader.AggregateEvents(ctx, query); !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation lost: %v", err)
	}
}

func TestAggregateEventsSupportsIndependentBucketLimitAndAllFields(t *testing.T) {
	reader := newTestReader(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Aggregations map[string]struct {
				Composite struct {
					Size    int              `json:"size"`
					Sources []map[string]any `json:"sources"`
				} `json:"composite"`
			} `json:"aggregations"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		group := body.Aggregations[ESAggregationGroups].Composite
		if group.Size != event.MaxAggregationBuckets+1 || len(group.Sources) != len(event.AggregationFields()) {
			t.Errorf("aggregate limit/field count = %+v", group)
		}
		_, _ = w.Write([]byte(`{"aggregations":{"groups":{"buckets":[]}}}`))
	}, nil)
	_, err := reader.AggregateEvents(t.Context(), &event.AggregationQuery{
		Filter:  &event.Query{Region: "us", StartTime: time.Unix(100, 0), EndTime: time.Unix(200, 0)},
		GroupBy: event.AggregationFields(), Limit: event.MaxAggregationBuckets,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestQueryScopesEveryFixedPredicate(t *testing.T) {
	start := time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC)
	query, err := buildQuery(&event.Query{
		Region: "cn", Hostnames: []string{"node-1", "node-2"}, ContainerHostnames: []string{"pod-1"},
		ContainerHostNamespaces: []string{"production"}, TracerNames: []string{"cpusys"},
		StartTime: start, EndTime: start.Add(time.Hour), ProfileOnly: true,
	}, testFieldSchema())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(query)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Bool struct {
			Filter []struct {
				Terms  map[string][]string          `json:"terms"`
				Range  map[string]map[string]string `json:"range"`
				Exists *struct {
					Field string `json:"field"`
				} `json:"exists"`
			} `json:"filter"`
		} `json:"bool"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	terms := make(map[string][]string)
	var bounds map[string]string
	for _, clause := range decoded.Bool.Filter {
		for field, values := range clause.Terms {
			terms[field] = values
		}
		if clause.Range != nil {
			bounds = clause.Range[types.DocumentFieldUploadedTimestamp]
		}
	}
	if len(terms) != 5 || len(terms[types.DocumentFieldHostname+".keyword"]) != 2 || terms[types.DocumentFieldRegion+".keyword"][0] != "cn" || strings.Contains(string(encoded), "tracer_type") {
		t.Fatalf("unscoped query: %s", encoded)
	}
	if bounds["gte"] == "" || bounds["lte"] == "" || bounds["format"] != "strict_date_optional_time_nanos" || !strings.Contains(string(encoded), ProfileFramesField) || !strings.Contains(string(encoded), ProfileSamplesField) {
		t.Fatalf("missing time/profile predicates: %s", encoded)
	}
}

func TestEmptyRegionDoesNotRestrictQuery(t *testing.T) {
	for _, filter := range []*event.Query{nil, {}, {Hostnames: []string{"node-1"}}} {
		query, err := buildQuery(filter, testFieldSchema())
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(query)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), types.DocumentFieldRegion+".keyword") {
			t.Fatalf("empty region filter added: %s", encoded)
		}
	}
}

func TestQueriesIgnoreTracerTypeButKeepProfilePayloadConstraint(t *testing.T) {
	for _, profileOnly := range []bool{false, true} {
		query, err := buildQuery(&event.Query{Region: "us01", TracerNames: []string{"cpuidle"}, ProfileOnly: profileOnly, StartTime: time.Unix(100, 0), EndTime: time.Unix(200, 0)}, testFieldSchema())
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(query)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "tracer_type") || !strings.Contains(string(encoded), "region.keyword") || !strings.Contains(string(encoded), "uploaded_timestamp") || strings.Contains(string(encoded), ProfileFramesField) != profileOnly {
			t.Fatalf("incorrect type-independent query: %s", encoded)
		}
	}
}

func TestProfileQueryScopesRecordIDWithOtherPredicates(t *testing.T) {
	filter := &event.Query{Region: "us01", ContainerHostnames: []string{"target"}, TracerNames: []string{"cpuidle"}, RecordID: "profile-18-17", StartTime: time.Unix(100, 0), EndTime: time.Unix(200, 0), ProfileOnly: true}
	query, err := buildQuery(filter, testFieldSchema())
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(query)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"ids":{"values":["profile-18-17"]}`, `"region.keyword"`, `"container_hostname.keyword"`, `"tracer_name.keyword"`, `"uploaded_timestamp"`, ProfileFramesField} {
		if !strings.Contains(string(encoded), required) {
			t.Fatalf("record ID query lost %s: %s", required, encoded)
		}
	}
	filter.RecordID = strings.Repeat("x", event.MaxRecordIDLength+1)
	if err := validateQuery(filter); !errors.Is(err, event.ErrInvalidQuery) {
		t.Fatalf("oversized record ID accepted: %v", err)
	}
}

func TestSearchPreservesArbitraryOrMissingTracerType(t *testing.T) {
	for _, tracerType := range []string{"auto", "autotracing", "event", "manual", "task", "profiling", "custom", ""} {
		t.Run(tracerType, func(t *testing.T) {
			reader := newTestReader(t, func(w http.ResponseWriter, r *http.Request) {
				var query map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&query); err != nil {
					t.Error(err)
				}
				if strings.Contains(string(query["query"]), "tracer_type") {
					t.Error("query restricted producer type")
				}
				source := map[string]any{"region": "us01", "tracer_name": "cpuidle", "uploaded_time": "2026-10-12T10:10:00Z"}
				if tracerType != "" {
					source["tracer_type"] = tracerType
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"hits": map[string]any{"hits": []any{map[string]any{"_id": "record", "_source": source}}}})
			}, nil)
			result, err := reader.SearchEvents(t.Context(), &event.Query{Region: "us01", TracerNames: []string{"cpuidle"}, Limit: 1})
			if err != nil || len(result.Documents) != 1 || result.Documents[0].TracerRunType != tracerType {
				t.Fatalf("type=%q result=%+v error=%v", tracerType, result, err)
			}
		})
	}
}

func TestReaderRejectsUnboundedInputsBeforeNetwork(t *testing.T) {
	reader := newTestReader(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("invalid query reached backend")
		w.WriteHeader(http.StatusBadRequest)
	}, nil)
	for _, filter := range []*event.Query{
		{Offset: -1},
		{Offset: event.MaxDocuments + 1},
		{Limit: -1},
		{Hostnames: make([]string, event.MaxTargetValues+1)},
		{TracerNames: []string{strings.Repeat("x", event.MaxTracerNameLength+1)}},
		{StartTime: time.Now().Add(time.Hour), EndTime: time.Now()},
	} {
		if _, err := reader.SearchEvents(t.Context(), filter); !errors.Is(err, event.ErrInvalidQuery) {
			t.Fatalf("invalid filter accepted: %+v, %v", filter, err)
		}
	}
}

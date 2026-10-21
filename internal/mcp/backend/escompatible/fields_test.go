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

package escompatible

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/document"
	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/constant"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/pkg/types"
)

func capsFixture(native bool) fieldCapsResponse {
	response := fieldCapsResponse{Fields: make(map[string]map[string]fieldCapability)}
	for _, name := range []string{types.DocumentFieldRegion, types.DocumentFieldHostname, types.DocumentFieldContainerHostname, types.DocumentFieldContainerHostNamespace, types.DocumentFieldTracerName} {
		keyword := fieldCapability{Type: "keyword", Searchable: true, Aggregatable: true}
		if native {
			response.Fields[name] = map[string]fieldCapability{"keyword": keyword}
		} else {
			response.Fields[name] = map[string]fieldCapability{"text": {Type: "text", Searchable: true}}
			response.Fields[name+".keyword"] = map[string]fieldCapability{"keyword": keyword}
		}
	}
	for _, name := range []string{constant.ESUploadedTimeField, types.DocumentFieldUploadedTimestamp} {
		response.Fields[name] = map[string]fieldCapability{"date": {Type: "date", Searchable: true, Aggregatable: true}}
	}
	for _, group := range profileFieldGroups() {
		for _, name := range group {
			response.Fields[name] = map[string]fieldCapability{"keyword": {Type: "keyword", Searchable: true, Aggregatable: true}}
		}
	}
	return response
}

func TestReaderAdaptsNativeAndMultifieldKeywordsWithBothTimeFields(t *testing.T) {
	for _, native := range []bool{true, false} {
		t.Run(map[bool]string{true: "native", false: "multifield"}[native], func(t *testing.T) {
			var metadata atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, constant.ESFieldCapsPathSuffix) {
					metadata.Add(1)
					if !strings.Contains(r.URL.Query().Get("fields"), "uploaded_timestamp") || strings.Contains(r.URL.Query().Get("fields"), "observed_timestamp") || strings.Contains(r.URL.Query().Get("fields"), "tracer_type") || r.URL.Query().Get("include_unmapped") != "true" {
						t.Errorf("incorrect capabilities request: %s", r.URL)
					}
					_ = json.NewEncoder(w).Encode(capsFixture(native))
					return
				}
				var body map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
				}
				query := string(body["query"])
				field := "region"
				if !native {
					field += ".keyword"
				}
				if !strings.Contains(query, `"`+field+`"`) || !strings.Contains(query, `"uploaded_time"`) || !strings.Contains(query, `"uploaded_timestamp"`) || strings.Contains(query, "observed_timestamp") || strings.Contains(query, "tracer_type") {
					t.Errorf("incorrect mapped/time query: %s", query)
				}
				switch r.URL.Path {
				case "/events/_count":
					_, _ = w.Write([]byte(`{"count":2}`))
				case "/events/_search":
					if _, ok := body["aggregations"]; ok {
						tracerField := "tracer_name"
						if !native {
							tracerField += ".keyword"
						}
						if !strings.Contains(string(body["aggregations"]), `"field":"`+tracerField+`"`) {
							t.Errorf("incorrect aggregation: %s", body["aggregations"])
						}
						_, _ = w.Write([]byte(`{"aggregations":{"terms":{"buckets":[{"key":"cpusys","doc_count":2}]}}}`))
					} else {
						if !strings.Contains(string(body["sort"]), "_script") || !strings.Contains(string(body["sort"]), "uploaded_timestamp") || strings.Contains(string(body["sort"]), "observed_timestamp") {
							t.Errorf("mixed-time sort: %s", body["sort"])
						}
						_, _ = w.Write([]byte(`{"hits":{"hits":[{"_id":"new","_source":{"region":"cn","observed_timestamp":"2099-01-01T00:00:00Z","uploaded_timestamp":"2026-10-12T10:20:00Z"}},{"_id":"old","_source":{"region":"cn","uploaded_time":"2026-10-12T18:10:00+08:00"}}]}}`))
					}
				default:
					t.Errorf("unexpected endpoint: %s", r.URL)
				}
			}))
			defer server.Close()
			reader, err := New(&Config{Addresses: []string{server.URL}, Index: "events"})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = reader.Close(t.Context()) }()
			if metadata.Load() != 0 {
				t.Fatal("constructor fetched metadata")
			}
			filter := &event.Query{Region: "cn", StartTime: time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 10, 12, 11, 0, 0, 0, time.UTC), Limit: 10}
			count, err := reader.Count(t.Context(), filter)
			if err != nil || count != 2 {
				t.Fatalf("count=%d error=%v", count, err)
			}
			tracers, err := reader.TracerCounts(t.Context(), filter)
			if err != nil || len(tracers) != 1 || tracers[0].Name != "cpusys" || tracers[0].Count != 2 {
				t.Fatalf("tracers=%+v error=%v", tracers, err)
			}
			result, err := reader.SearchEvents(t.Context(), filter)
			if err != nil || len(result.Documents) != 2 {
				t.Fatalf("search=%+v error=%v", result, err)
			}
			if !result.Documents[0].QueryTimestamp().After(result.Documents[1].QueryTimestamp()) || result.Documents[0].UploadedTimestamp.IsZero() {
				t.Fatalf("wrong query clocks: %+v", result.Documents)
			}
			encoded, err := json.Marshal(result.Documents[0])
			if err != nil {
				t.Fatal(err)
			}
			var clocks struct {
				Observed time.Time `json:"observed_timestamp"`
				Uploaded time.Time `json:"uploaded_timestamp"`
				Query    time.Time `json:"timestamp"`
			}
			if err := json.Unmarshal(encoded, &clocks); err != nil || clocks.Observed.Year() != 2099 || !clocks.Uploaded.Equal(clocks.Query) || !clocks.Query.Equal(time.Date(2026, 10, 12, 10, 20, 0, 0, time.UTC)) {
				t.Fatalf("upload clock or metadata lost: %s, %v", encoded, err)
			}
			if metadata.Load() != 1 {
				t.Fatalf("metadata requests=%d", metadata.Load())
			}
		})
	}
}

func TestFieldCapabilitiesScopesMixedKeywordMappingsByIndex(t *testing.T) {
	response := capsFixture(true)
	for _, field := range []string{types.DocumentFieldRegion, types.DocumentFieldTracerName} {
		response.Fields[field] = map[string]fieldCapability{
			"keyword": {Type: "keyword", Searchable: true, Aggregatable: true, Indices: []string{"native"}},
			"text":    {Type: "text", Searchable: true, Indices: []string{"text"}},
		}
		response.Fields[field+".keyword"] = map[string]fieldCapability{"keyword": {Type: "keyword", Searchable: true, Aggregatable: true, Indices: []string{"text"}}}
	}
	schema, err := resolveFieldSchema(&response)
	if err != nil {
		t.Fatal(err)
	}
	clause, err := schema.terms(types.DocumentFieldRegion, []string{"us01"})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(clause)
	if !strings.Contains(string(encoded), `"_index":["native"]`) || !strings.Contains(string(encoded), `"_index":["text"]`) || !strings.Contains(string(encoded), `"region.keyword":["us01"]`) {
		t.Fatalf("unscoped keyword variants: %s", encoded)
	}
	reader := newTestReader(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Aggregations map[string]json.RawMessage `json:"aggregations"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/events/_search" {
			t.Errorf("aggregation bypassed configured selector: %s", r.URL.Path)
		}
		if !strings.Contains(string(body.Aggregations["terms"]), `"script"`) || strings.Contains(string(body.Aggregations["terms"]), `"field":"tracer_name.keyword"`) {
			t.Errorf("mixed aggregation must not load fielddata: %s", body.Aggregations["terms"])
		}
		_, _ = w.Write([]byte(`{"aggregations":{"terms":{"buckets":[{"key":"cpusys","doc_count":5}]}}}`))
	}, nil)
	reader.fields = schema
	counts, err := reader.TracerCounts(t.Context(), &event.Query{Region: "us01"})
	if err != nil || len(counts) != 1 || counts[0].Count != 5 {
		t.Fatalf("mixed counts=%+v, %v", counts, err)
	}
}

func TestFieldCapabilitiesCacheCancellationAndRefresh(t *testing.T) {
	var requests atomic.Int64
	entered, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			close(entered)
			<-release
		}
		_ = json.NewEncoder(w).Encode(capsFixture(true))
	}))
	defer server.Close()
	reader, err := New(&Config{Addresses: []string{server.URL}, Index: "events"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(t.Context()) }()
	var wait sync.WaitGroup
	wait.Add(1)
	go func() {
		defer wait.Done()
		if _, err := reader.queryFields(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := reader.queryFields(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("metadata waiter ignored cancellation: %v", err)
	}
	close(release)
	wait.Wait()
	for range 3 {
		if _, err := reader.queryFields(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("cache requests=%d", requests.Load())
	}
	reader.fieldsAt = time.Now().Add(-2 * constant.ESFieldCapsCacheTTL)
	if _, err := reader.queryFields(t.Context()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("stale metadata was not refreshed: %d", requests.Load())
	}
}

func TestDecodeDocumentUsesUploadClock(t *testing.T) {
	for _, payload := range []string{
		`{"time":"2026-10-12T18:10:00+08:00"}`,
		`{"uploaded_timestamp":null,"uploaded_time":null,"time":"2026-10-12T10:10:00Z"}`,
		`{"uploaded_time":"2026-10-12T18:10:00+08:00"}`,
		`{"uploaded_time":"2026-10-12 18:10:00 +0800"}`,
		`{"uploaded_timestamp":"2026-10-12 10:10:00"}`,
		`{"uploaded_time":"2026-10-12T10:10:00Z","time":"2099-01-01T00:00:00Z"}`,
		`{"uploaded_timestamp":null,"uploaded_time":"2026-10-12T18:10:00+08:00"}`,
		`{"uploaded_timestamp":"2026-10-12T10:10:00Z"}`,
		`{"observed_timestamp":"2099-01-01T00:00:00Z","uploaded_timestamp":"2026-10-12T10:10:00Z"}`,
		`{"uploaded_timestamp":"2026-10-12T10:10:00Z","uploaded_time":"2099-01-01T00:00:00Z"}`,
		`{"uploaded_timestamp":"2026-10-12T10:10:00Z","uploaded_time":"2099-01-01T00:00:00Z","time":"2099-01-02T00:00:00Z"}`,
	} {
		var document event.Document
		if err := decodeDocument([]byte(payload), &document); err != nil {
			t.Fatal(err)
		}
		if document.QueryTimestamp().Format(time.RFC3339) != "2026-10-12T10:10:00Z" {
			t.Fatalf("wrong upload clock: %+v", document)
		}
	}
	for _, payload := range []string{
		`{"observed_timestamp":"2026-10-12T10:10:00Z"}`,
		`{"uploaded_timestamp":"invalid","uploaded_time":"2026-10-12T10:10:00Z"}`,
		`{"uploaded_time":"invalid","time":"2026-10-12T10:10:00Z"}`,
		`{"uploaded_timestamp":null}`,
		`{"uploaded_timestamp":null,"uploaded_time":null,"time":null}`,
	} {
		if err := decodeDocument([]byte(payload), &event.Document{}); err == nil {
			t.Fatalf("invalid upload clock accepted: %s", payload)
		}
	}
}

func TestDecodeDocumentPreservesPayloadNumbers(t *testing.T) {
	const payload = `{"large":9007199254740993,"signed":9223372036854775807,"unsigned":18446744073709551615,"nested":[-9223372036854775808,1.25,1e3],"text":"9007199254740993"}`
	raw := []byte(`{"uploaded_timestamp":"2026-10-12T10:10:00Z","tracer_data":` + payload + `}`)
	var document event.Document
	if err := decodeDocument(raw, &document); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(document.TracerData)
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(payload), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("payload changed: got %s, want %s", encoded, payload)
	}
}

func TestDecodeCurrentAutotracingDocument(t *testing.T) {
	started := timeutil.Timestamp{Time: time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)}
	metadata, err := document.New("cn").Build(&document.Input{
		TracerName: "cpusys", StartedTimestamp: started, TracerRunType: types.TracerRunTypeAutotracing,
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata.UploadedTimestamp = timeutil.Timestamp{Time: started.Add(time.Minute)}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	var decoded event.Document
	if err := decodeDocument(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if !decoded.QueryTimestamp().Equal(metadata.UploadedTimestamp.Time) || decoded.StartedTimestamp == nil || !decoded.StartedTimestamp.Equal(started.Time) {
		t.Fatalf("current autotracing clock or metadata lost: %+v", decoded)
	}
}

func TestDecodeDocumentAcceptsIntegerEpochMillis(t *testing.T) {
	for _, millis := range []int64{0, -1, 1768471800123, 1768471800, 253402300799999} {
		for _, quoted := range []bool{false, true} {
			for _, field := range queryTimeFieldNames() {
				value := fmt.Sprint(millis)
				if quoted {
					value = fmt.Sprintf("%q", value)
				}
				var document event.Document
				if err := decodeDocument([]byte(fmt.Sprintf(`{%q:%s}`, field, value)), &document); err != nil {
					t.Fatalf("field=%s value=%s error=%v", field, value, err)
				}
				want := time.UnixMilli(millis).UTC()
				if !document.QueryTimestamp().Equal(want) {
					t.Fatalf("field=%s value=%s timestamp=%s want=%s", field, value, document.QueryTimestamp(), want)
				}
				if field == types.DocumentFieldUploadedTimestamp && !document.UploadedTimestamp.Equal(want) {
					t.Fatalf("typed uploaded_timestamp was not normalized: %+v", document)
				}
			}
		}
	}
	for _, raw := range []string{`9223372036854775808`, `9223372036854775807`, `-9223372036854775808`, `1.25`, `1e3`, `"1e3"`, `null`, `true`, `[]`, `{}`} {
		if _, err := decodeTimestamp(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid or unrepresentable epoch_millis accepted: %s", raw)
		}
	}
}

func TestProfileFieldCapabilitiesCoverEveryPayloadMarker(t *testing.T) {
	names := schemaFieldNames()
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] {
			t.Fatalf("duplicate capability field: %s", name)
		}
		seen[name] = true
	}
	for _, group := range profileFieldGroups() {
		for _, name := range group {
			if !seen[name] {
				t.Fatalf("profile capability field missing: %s", name)
			}
		}
	}
}

func TestProfileMappingCoverageSelectsBoundedSourceReads(t *testing.T) {
	for _, test := range []struct {
		name    string
		mode    string
		indexed bool
	}{
		{"fully indexed", "all", true},
		{"unmapped payload", "none", false},
		{"index false without doc values", "unindexed", false},
		{"index false with doc values", "doc-values", true},
		{"searchable text marker", "text", true},
		{"frame and pprof indices", "mixed", true},
		{"partially indexed payload", "partial", false},
		{"pprof markers on disjoint indices", "disjoint", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			caps := capsFixture(true)
			caps.Indices = []string{"frames", "pprof"}
			if test.mode != "all" {
				for _, group := range profileFieldGroups() {
					for _, name := range group {
						delete(caps.Fields, name)
					}
				}
			}
			switch test.mode {
			case "unindexed", "doc-values", "text":
				kind := "keyword"
				if test.mode == "text" {
					kind = "text"
				}
				caps.Fields[constant.ProfileFramesField] = map[string]fieldCapability{kind: {
					Searchable: test.mode == "text", Aggregatable: test.mode == "doc-values",
				}}
			case "mixed", "partial", "disjoint":
				caps.Fields[constant.ProfileFramesField] = map[string]fieldCapability{"keyword": {
					Searchable: true, Indices: []string{"frames"},
				}}
				if test.mode != "partial" {
					for index, name := range profileFieldGroups()[1] {
						indices := []string{"pprof"}
						if test.mode == "disjoint" && index == 0 {
							indices = []string{"frames"}
						}
						caps.Fields[name] = map[string]fieldCapability{"keyword": {Searchable: true, Indices: indices}}
					}
				}
			}
			schema, err := resolveFieldSchema(&caps)
			if err != nil {
				t.Fatal(err)
			}
			if schema.sourceProfile == test.indexed {
				t.Fatalf("sourceProfile=%t want=%t", schema.sourceProfile, !test.indexed)
			}
			filter := &event.Query{Region: "cn"}
			if schema.needsSource(filter, false) {
				t.Fatal("payload mapping changed ordinary event reads")
			}
			filter.ProfileOnly = true
			for _, catalog := range []bool{false, true} {
				if got := schema.needsSource(filter, catalog); got == test.indexed {
					t.Fatalf("catalog=%t source fallback=%t want=%t", catalog, got, !test.indexed)
				}
			}
		})
	}
}

func TestTimeRangeMatchesAnyUploadClock(t *testing.T) {
	response := capsFixture(true)
	response.Fields[constant.ESTimeField] = map[string]fieldCapability{"date": {Type: "date", Searchable: true, Aggregatable: true}}
	schema, err := resolveFieldSchema(&response)
	if err != nil {
		t.Fatal(err)
	}
	bounds := map[string]string{"gte": "2026-10-12T10:00:00Z", "lte": "2026-10-12T11:00:00Z"}
	parameters := map[string]string{"gte": bounds["gte"], "lte": bounds["lte"], "format": "strict_date_optional_time_nanos"}
	want := map[string]any{"bool": map[string]any{
		"should": []any{
			map[string]any{"range": map[string]any{"uploaded_timestamp": parameters}},
			map[string]any{"range": map[string]any{"uploaded_time": parameters}},
			map[string]any{"range": map[string]any{"time": parameters}},
		},
		"minimum_should_match": 1,
	}}
	if got := schema.timeRange(bounds); !reflect.DeepEqual(got, want) {
		t.Fatalf("time filter does not allow all upload clocks: %+v", got)
	}
	if len(bounds) != 2 {
		t.Fatalf("time filter mutated caller bounds: %+v", bounds)
	}
}

func TestReaderSupportsTimeOnlyIndex(t *testing.T) {
	reader := newTestReader(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, constant.ESFieldCapsPathSuffix) {
			found := false
			for _, field := range strings.Split(r.URL.Query().Get("fields"), ",") {
				found = found || field == constant.ESTimeField
			}
			if !found {
				t.Errorf("time missing from field capabilities request: %s", r.URL)
			}
			response := capsFixture(true)
			delete(response.Fields, types.DocumentFieldUploadedTimestamp)
			delete(response.Fields, constant.ESUploadedTimeField)
			response.Fields[constant.ESTimeField] = map[string]fieldCapability{"date": {Type: "date", Searchable: true, Aggregatable: true}}
			_ = json.NewEncoder(w).Encode(response)
			return
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if !strings.Contains(string(body["query"]), `"range":{"time":`) || !strings.Contains(string(body["sort"]), `"time":{"order":"desc"`) {
			t.Errorf("query or sorting did not use time: %+v", body)
		}
		_, _ = w.Write([]byte(`{"hits":{"hits":[{"_id":"legacy","_source":{"time":"2026-10-12T10:10:00Z","region":"cn"}}]}}`))
	}, nil)
	reader.fields = nil
	result, err := reader.SearchEvents(t.Context(), &event.Query{Region: "cn", StartTime: time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC), Limit: 1})
	if err != nil || len(result.Documents) != 1 {
		t.Fatalf("time-only search: %+v, %v", result, err)
	}
	if result.Documents[0].QueryTimestamp().Format(time.RFC3339) != "2026-10-12T10:10:00Z" {
		t.Fatalf("wrong time fallback: %+v", result.Documents[0])
	}
}

func TestFieldCapabilitiesExpandsOnlyUnresolvedPrefixAndRetriesFailures(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch len(paths) {
		case 1:
			_, _ = w.Write([]byte(`{"fields":{}}`))
		case 2:
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case 3:
			_, _ = w.Write([]byte(`{"fields":{}}`))
		case 4:
			_ = json.NewEncoder(w).Encode(capsFixture(true))
		default:
			t.Errorf("unexpected cached metadata request: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	reader, err := New(&Config{Addresses: []string{server.URL}, Index: "relay"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(t.Context()) }()
	if _, err := reader.queryFields(t.Context()); err == nil {
		t.Fatal("metadata failure hidden")
	}
	if len(paths) != 2 {
		t.Fatalf("implicit metadata retries=%v", paths)
	}
	if _, err := reader.queryFields(t.Context()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(paths, ",") != "/relay/_field_caps,/relay*/_field_caps,/relay/_field_caps,/relay*/_field_caps" {
		t.Fatalf("metadata index scopes=%v", paths)
	}
}

func TestFieldCapabilitiesDoesNotBroadenExistingIndex(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte(`{"indices":["events"],"fields":{}}`))
	}))
	defer server.Close()
	reader, err := New(&Config{Addresses: []string{server.URL}, Index: "events"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close(t.Context()) }()
	if fields, err := reader.queryFields(t.Context()); err != nil || !fields.sourceTime || !fields.sourceFields[types.DocumentFieldRegion] {
		t.Fatalf("unindexed source mapping did not select compatibility reads: %+v, %v", fields, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("existing index broadened to wildcard: %d", requests.Load())
	}
}

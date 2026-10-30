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
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/pkg/types"
)

func sourceTestSchema(t *testing.T) *fieldSchema {
	t.Helper()
	caps := capsFixture(true)
	for name := range caps.Fields {
		caps.Fields[name] = map[string]fieldCapability{"text": {Type: "text", Searchable: true}}
	}
	schema, err := resolveFieldSchema(&caps)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func sourceTestReader(t *testing.T, hits []any) *Reader {
	t.Helper()
	reader := newTestReader(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/events/_search" {
			t.Errorf("compatibility read left configured selector: %s %s", r.Method, r.URL.Path)
		}
		var body struct {
			Query json.RawMessage `json:"query"`
			Size  int             `json:"size"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Size != MaxSourceScanDocuments+1 || strings.Contains(string(body.Query), `"range"`) || strings.Contains(string(body.Query), `"terms"`) {
			t.Errorf("unsafe or unbounded compatibility read: %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"hits": map[string]any{"hits": hits}})
	}, nil)
	reader.fields = sourceTestSchema(t)
	return reader
}

func TestSourceQueriesUseExactOriginalValuesAndAllPredicates(t *testing.T) {
	base := map[string]any{"region": "us01", "hostname": "node-a", "container_hostname": "pod-a", "container_host_namespace": "ns-a", "tracer_name": "cpu-idle", "uploaded_time": "2026-10-12 10:10:00 +0000"}
	hits := []any{map[string]any{"_id": "match-old", "_source": base}}
	for field, value := range map[string]any{"region": "us02", "hostname": "node a", "container_hostname": "pod a", "container_host_namespace": "ns a", "tracer_name": "cpu idle", "uploaded_time": "2026-10-12T12:00:00Z"} {
		source := make(map[string]any, len(base))
		for k, v := range base {
			source[k] = v
		}
		source[field] = value
		hits = append(hits, map[string]any{"_id": "wrong-" + field, "_source": source})
	}
	newer := make(map[string]any, len(base))
	for k, v := range base {
		newer[k] = v
	}
	newer["uploaded_time"] = "2026-10-12T10:20:00Z"
	hits = append(hits, map[string]any{"_id": "match-new", "_source": newer})
	reader := sourceTestReader(t, hits)
	q := &event.Query{Region: "us01", Hostnames: []string{"other", "node-a"}, ContainerHostnames: []string{"pod-a"}, ContainerHostNamespaces: []string{"ns-a"}, TracerNames: []string{"cpu-idle"}, StartTime: time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 10, 12, 11, 0, 0, 0, time.UTC), Limit: 1}
	count, err := reader.Count(t.Context(), q)
	if err != nil || count != 2 {
		t.Fatalf("count=%d error=%v", count, err)
	}
	counts, err := reader.TracerCounts(t.Context(), q)
	if err != nil || !reflect.DeepEqual(counts, []event.TracerCount{{Name: "cpu-idle", Count: 2}}) {
		t.Fatalf("counts=%+v error=%v", counts, err)
	}
	result, err := reader.SearchEvents(t.Context(), q)
	if err != nil || len(result.Documents) != 1 || result.Documents[0].RecordID != "match-new" || !result.Truncated || !result.CountExact || result.TotalCount == nil || *result.TotalCount != 2 {
		t.Fatalf("search=%+v error=%v", result, err)
	}
	q.Offset = 1
	result, err = reader.SearchEvents(t.Context(), q)
	if err != nil || len(result.Documents) != 1 || result.Documents[0].RecordID != "match-old" || result.Truncated {
		t.Fatalf("offset search=%+v error=%v", result, err)
	}
}

func TestSourceReadsRejectIncompleteCandidateSets(t *testing.T) {
	hits := make([]any, MaxSourceScanDocuments+1)
	for i := range hits {
		hits[i] = map[string]any{"_id": fmt.Sprint(i), "_source": map[string]any{"uploaded_time": "2026-10-12T10:10:00Z"}}
	}
	reader := sourceTestReader(t, hits)
	_, err := reader.Count(t.Context(), &event.Query{})
	var retryable event.RetryableError
	if !errors.Is(err, event.ErrIncompleteData) || !errors.As(err, &retryable) || retryable.Retryable() {
		t.Fatalf("candidate limit was not a permanent incomplete-data error: %v", err)
	}
	if !strings.Contains(err.Error(), "narrow") {
		t.Fatalf("candidate limit has no actionable guidance: %v", err)
	}
	reader = sourceTestReader(t, hits[:MaxSourceScanDocuments])
	count, err := reader.Count(t.Context(), &event.Query{})
	if err != nil || count != int64(MaxSourceScanDocuments) {
		t.Fatalf("exactly-at-limit candidate set rejected: count=%d error=%v", count, err)
	}
}

func TestSourceProfileReadsNormalizeBothJSONEncodings(t *testing.T) {
	for _, protobufJSON := range []bool{false, true} {
		t.Run(fmt.Sprint(protobufJSON), func(t *testing.T) {
			var source map[string]any
			if err := json.Unmarshal(profileSourceJSON(t, testPprofProfile(), protobufJSON), &source); err != nil {
				t.Fatal(err)
			}
			source["uploaded_time"] = "2026-10-12T10:10:00Z"
			source["tracer_name"] = "cpu-idle"
			reader := sourceTestReader(t, []any{map[string]any{"_id": "profile", "_source": source}, map[string]any{"_id": "empty", "_source": map[string]any{"tracer_data": map[string]any{"flamedata": map[string]any{"profile": map[string]any{"sample": []any{}, "location": []any{}, "function": []any{}, "sample_type": []any{}}}}}}})
			q := &event.Query{ProfileOnly: true, Limit: 1}
			count, err := reader.Count(t.Context(), q)
			if err != nil || count != 1 {
				t.Fatalf("profile count=%d error=%v", count, err)
			}
			result, err := reader.SearchEvents(t.Context(), q)
			if err != nil || len(result.Documents) != 1 || len(result.Documents[0].ProfileFrames) == 0 || result.Documents[0].ProfileSampleUnit != "nanoseconds" || len(result.Warnings) > 0 {
				t.Fatalf("profile=%+v error=%v", result, err)
			}
		})
	}
}

func TestSourceReadCancellationAndMissingSource(t *testing.T) {
	reader := sourceTestReader(t, []any{map[string]any{"_id": "no-source"}})
	if _, err := reader.Count(t.Context(), &event.Query{}); !errors.Is(err, event.ErrDecodeFailed) {
		t.Fatalf("missing source accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := reader.Count(ctx, &event.Query{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestSourceCompatibilityRetainsSafeIndexedPredicates(t *testing.T) {
	caps := capsFixture(true)
	caps.Fields[types.DocumentFieldTracerName] = map[string]fieldCapability{"text": {Type: "text", Searchable: true}}
	schema, err := resolveFieldSchema(&caps)
	if err != nil {
		t.Fatal(err)
	}
	reader := newTestReader(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query json.RawMessage `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		query := string(body.Query)
		for _, expected := range []string{`"region"`, `"hostname"`, `"range"`, `"ids"`} {
			if !strings.Contains(query, expected) {
				t.Errorf("safe predicate %s lost: %s", expected, query)
			}
		}
		if strings.Contains(query, `"tracer_name"`) {
			t.Errorf("text predicate sent to ES: %s", query)
		}
		_, _ = w.Write([]byte(`{"hits":{"hits":[]}}`))
	}, nil)
	reader.fields = schema
	_, err = reader.Count(t.Context(), &event.Query{Region: "us01", Hostnames: []string{"node-a"}, TracerNames: []string{"cpu-idle"}, RecordID: "record", StartTime: time.Unix(100, 0), EndTime: time.Unix(200, 0)})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSourceTimeCoverageIncludesUnindexedAndConflictingIndices(t *testing.T) {
	for _, test := range []struct {
		name   string
		fields map[string]map[string]fieldCapability
		want   bool
	}{
		{"legacy fallback", map[string]map[string]fieldCapability{
			"uploaded_timestamp": {"date": {Searchable: true, Aggregatable: true, Indices: []string{"new"}}, "unmapped": {Indices: []string{"old"}}},
			"uploaded_time":      {"date": {Searchable: true, Aggregatable: true, Indices: []string{"old"}}, "unmapped": {Indices: []string{"new"}}},
		}, false},
		{"source-only legacy", map[string]map[string]fieldCapability{
			"uploaded_timestamp": {"date": {Searchable: true, Aggregatable: true, Indices: []string{"new"}}, "unmapped": {Indices: []string{"old"}}},
		}, true},
		{"date text conflict", map[string]map[string]fieldCapability{
			"uploaded_time": {"date": {Searchable: true, Aggregatable: true, Indices: []string{"new"}}, "text": {Searchable: true, Indices: []string{"old"}}},
		}, true},
		{"no time mappings", map[string]map[string]fieldCapability{}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			schema, err := resolveFieldSchema(&fieldCapsResponse{Indices: []string{"new", "old"}, Fields: test.fields})
			if err != nil || schema.sourceTime != test.want {
				t.Fatalf("source time=%+v want=%t error=%v", schema, test.want, err)
			}
		})
	}
}

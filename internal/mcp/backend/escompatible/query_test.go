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
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/mcp/backend/event"
	"github.com/ccfos/huatuo/internal/mcp/constant"
	"github.com/ccfos/huatuo/pkg/types"
)

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
	if bounds["gte"] == "" || bounds["lte"] == "" || bounds["format"] != "strict_date_optional_time_nanos" || !strings.Contains(string(encoded), constant.ProfileFramesField) || !strings.Contains(string(encoded), constant.ProfileSamplesField) {
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
		if strings.Contains(string(encoded), "tracer_type") || !strings.Contains(string(encoded), "region.keyword") || !strings.Contains(string(encoded), "uploaded_timestamp") || strings.Contains(string(encoded), constant.ProfileFramesField) != profileOnly {
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
	for _, required := range []string{`"ids":{"values":["profile-18-17"]}`, `"region.keyword"`, `"container_hostname.keyword"`, `"tracer_name.keyword"`, `"uploaded_timestamp"`, constant.ProfileFramesField} {
		if !strings.Contains(string(encoded), required) {
			t.Fatalf("record ID query lost %s: %s", required, encoded)
		}
	}
	filter.RecordID = strings.Repeat("x", constant.MaxRecordIDLength+1)
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
		{Offset: constant.MaxDocuments + 1},
		{Limit: -1},
		{Hostnames: make([]string, constant.MaxTargetValues+1)},
		{TracerNames: []string{strings.Repeat("x", constant.MaxTracerNameLength+1)}},
		{StartTime: time.Now().Add(time.Hour), EndTime: time.Now()},
	} {
		if _, err := reader.SearchEvents(t.Context(), filter); !errors.Is(err, event.ErrInvalidQuery) {
			t.Fatalf("invalid filter accepted: %+v, %v", filter, err)
		}
	}
}

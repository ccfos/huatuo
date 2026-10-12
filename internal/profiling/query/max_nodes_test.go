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
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"

	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
)

func TestSelectMergeStacktracesMaxNodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/":
			_, _ = w.Write([]byte(`{"version":{"number":"8.15.5"}}`))
		case r.Method == http.MethodDelete:
			_, _ = w.Write([]byte(`{"succeeded":true,"num_freed":1}`))
		case r.URL.Path == "/_search/scroll":
			_, _ = w.Write([]byte(`{"_scroll_id":"profiles","hits":{"hits":[]}}`))
		default:
			_, _ = w.Write([]byte(`{"_scroll_id":"profiles","hits":{"hits":[{"_id":"1","_source":{
				"hostname":"node-1","uploaded_timestamp":"2026-10-08T00:00:00Z",
				"started_timestamp":"2026-10-08T00:00:00Z","tracer_type":"profiling","tracer_id":"job-1",
				"profile_data":{"profile_type":"process_cpu:cpu:nanoseconds:cpu:nanoseconds","profile":{
					"string_table":["","cpu","nanoseconds","root","hot","cold"],
					"sample_type":[{"type":1,"unit":2}],"period_type":{"type":1,"unit":2},
					"function":[{"id":1,"name":3},{"id":2,"name":4},{"id":3,"name":5}],
					"location":[{"id":1,"line":[{"function_id":1}]},{"id":2,"line":[{"function_id":2}]},{"id":3,"line":[{"function_id":3}]}],
					"sample":[{"location_id":[2,1],"value":[10]},{"location_id":[3,1],"value":[1]}]
				}}
			}}]}}`))
		}
	}))
	defer server.Close()
	store, err := profilingstore.NewFromConfig(context.Background(), profilingstore.Config{Addresses: []string{server.URL}, Index: "profiles"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	service, err := NewProfileQueryService(store)
	if err != nil {
		t.Fatal(err)
	}
	zero, limit, large := int64(0), int64(2), int64(100)
	for _, tc := range []struct {
		name      string
		maxNodes  *int64
		truncated bool
	}{
		{"omitted", nil, false},
		{"zero", &zero, false},
		{"limited", &limit, true},
		{"larger than tree", &large, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := service.SelectMergeStacktraces(context.Background(), &querierv1.SelectMergeStacktracesRequest{
				LabelSelector: `{id="job-1"}`, ProfileTypeID: "process_cpu:cpu:nanoseconds:cpu:nanoseconds", MaxNodes: tc.maxNodes,
			})
			if err != nil {
				t.Fatal(err)
			}
			graph := result.Flamegraph
			if graph.Total != 11 || !slices.Contains(graph.Names, "hot") {
				t.Fatalf("total=%d, names=%v; want total 11 and the hot frame", graph.Total, graph.Names)
			}
			if slices.Contains(graph.Names, "other") != tc.truncated || slices.Contains(graph.Names, "cold") == tc.truncated {
				t.Errorf("names=%v, want truncated=%v", graph.Names, tc.truncated)
			}
		})
	}
}

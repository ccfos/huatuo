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
	"encoding/json"
	"fmt"
	"testing"

	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
)

func TestSelectMergeStacktracesInlineFrames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		lines string
		want  []string
	}{
		{"single function", `[{"function_id":1}]`, []string{"total", "root", "leaf"}},
		{"inlined function", `[{"function_id":1},{"function_id":2}]`, []string{"total", "root", "caller", "leaf"}},
		{"nested inlining", `[{"function_id":1},{"function_id":2},{"function_id":3}]`, []string{"total", "root", "outer", "caller", "leaf"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// pprof lists the innermost function first within each location.
			document := json.RawMessage(fmt.Sprintf(`{
				"hostname":"node-1","uploaded_timestamp":"2026-10-08T00:00:00Z",
				"started_timestamp":"2026-10-08T00:00:00Z","tracer_type":"profiling","tracer_id":"job-1",
				"profile_data":{"profile_type":"process_cpu:cpu:nanoseconds:cpu:nanoseconds","profile":{
					"string_table":["","cpu","nanoseconds","leaf","caller","outer","root"],
					"sample_type":[{"type":1,"unit":2}],"period_type":{"type":1,"unit":2},
					"function":[{"id":1,"name":3},{"id":2,"name":4},{"id":3,"name":5},{"id":4,"name":6}],
					"location":[{"id":1,"line":%s},{"id":2,"line":[{"function_id":4}]}],
					"sample":[{"location_id":[1,2],"value":[7]}]
				}}
			}`, tc.lines))
			store := newProfileQueryStore(t, 1, false, document)
			service, err := NewProfileQueryService(store)
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.SelectMergeStacktraces(context.Background(), &querierv1.SelectMergeStacktracesRequest{
				LabelSelector: `{id="job-1"}`,
				ProfileTypeID: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			})
			if err != nil {
				t.Fatal(err)
			}
			graph := result.Flamegraph
			if graph.Total != 7 || len(graph.Levels) != len(tc.want) {
				t.Fatalf("total=%d, levels=%d, names=%v; want total=7 and stack %v", graph.Total, len(graph.Levels), graph.Names, tc.want)
			}
			for i, want := range tc.want {
				values := graph.Levels[i].Values
				if len(values) != 4 {
					t.Fatalf("level %d has %d values, want one frame", i, len(values))
				}
				if got := graph.Names[values[3]]; got != want || values[1] != 7 {
					t.Errorf("level %d: name=%q, total=%d; want name=%q, total=7", i, got, values[1], want)
				}
			}
		})
	}
}

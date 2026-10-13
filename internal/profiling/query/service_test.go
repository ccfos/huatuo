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
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"

	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"

	"github.com/grafana/pyroscope/pkg/pprof"
	"github.com/prometheus/prometheus/model/labels"
)

func TestApplyProfileMatcherRegion(t *testing.T) {
	filter := &profilingstore.Filter{}
	matcher := &labels.Matcher{Name: "region", Value: "cn-beijing", Type: labels.MatchEqual}

	if err := applyProfileMatcher(filter, matcher); err != nil {
		t.Fatalf("applyProfileMatcher() error = %v", err)
	}
	if filter.Region != "cn-beijing" {
		t.Errorf("filter.Region = %q, want %q", filter.Region, "cn-beijing")
	}
}

func TestApplyProfileMatcherRejectsUnknownLabel(t *testing.T) {
	filter := &profilingstore.Filter{}
	matcher := &labels.Matcher{Name: "unknown", Value: "x", Type: labels.MatchEqual}

	if err := applyProfileMatcher(filter, matcher); err == nil {
		t.Fatal("applyProfileMatcher() error = nil, want error for unknown label")
	}
}

func TestProfileStringRejectsInvalidIndex(t *testing.T) {
	table := []string{"", "samples"}
	if got, ok := profileString(table, 1); !ok || got != "samples" {
		t.Fatalf("profileString(1)=(%q,%t), want (samples,true)", got, ok)
	}
	for _, index := range []int64{-1, 2, 100} {
		if got, ok := profileString(table, index); ok || got != "" {
			t.Errorf("profileString(%d)=(%q,%t), want empty,false", index, got, ok)
		}
	}
}

// The fake exercises the production mapper, batched storage, and aggregation together.
func newProfileQueryStore(t testing.TB, total int, fail bool) *profilingstore.Store {
	t.Helper()
	var cleared atomic.Bool
	position, size := 0, 0
	document := json.RawMessage(`{"hostname":"node-1","region":"test","uploaded_timestamp":"2026-10-08T00:00:00Z","started_timestamp":"2026-10-08T00:00:00Z","tracer_type":"profiling","tracer_id":"job-1","profile_data":{"profile_type":"process_cpu:cpu:nanoseconds:cpu:nanoseconds","profile":{"string_table":["","cpu","nanoseconds","work"],"sample_type":[{"type":1,"unit":2}],"period_type":{"type":1,"unit":2},"function":[{"id":1,"name":3}],"location":[{"id":1,"line":[{"function_id":1}]}],"sample":[{"location_id":[1],"value":[1]}]}}}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`{"version":{"number":"8.15.5"}}`))
			return
		}
		if r.Method == http.MethodDelete {
			cleared.Store(true)
			_, _ = w.Write([]byte(`{"succeeded":true,"num_freed":1}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/_search") {
			var request struct {
				Size int `json:"size"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
			}
			size = request.Size
			position = 0
		} else if r.URL.Path != "/_search/scroll" {
			http.Error(w, "unexpected path", http.StatusBadRequest)
			return
		}
		if fail && position > 0 {
			http.Error(w, `{"error":{"type":"search_context_missing_exception","reason":"expired scroll"}}`, http.StatusNotFound)
			return
		}
		hits := make([]map[string]any, 0, size)
		for len(hits) < size && position < total {
			hits = append(hits, map[string]any{"_id": fmt.Sprint(position), "_source": document})
			position++
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"_scroll_id": "profiles", "timed_out": false, "_shards": map[string]any{"failed": 0}, "hits": map[string]any{"hits": hits}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	store, err := profilingstore.NewFromConfig(context.Background(), &profilingstore.Config{Addresses: []string{server.URL}, Index: "profiles"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(context.Background()); err != nil {
			t.Error(err)
		}
		if !cleared.Load() {
			t.Error("query did not release its scroll")
		}
	})
	return store
}

func TestSelectMergeStacktracesConsumesAllBatches(t *testing.T) {
	for _, tc := range []struct {
		name    string
		total   int
		fail    bool
		wantErr bool
	}{
		{"hour of one-second windows", 3600, false, false},
		{"beyond result window", 10001, false, false},
		{"at total budget", maxProfileQueryRecords, false, false},
		{"over total budget", maxProfileQueryRecords + 1, false, true},
		{"later page fails", 200, true, true},
		{"empty", 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newProfileQueryStore(t, tc.total, tc.fail)
			service, err := NewProfileQueryService(store)
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.SelectMergeStacktraces(context.Background(), &querierv1.SelectMergeStacktracesRequest{LabelSelector: `{id="job-1"}`, ProfileTypeID: "process_cpu:cpu:nanoseconds:cpu:nanoseconds"})
			if tc.wantErr {
				if err == nil || result != nil {
					t.Fatalf("result=%v, error=%v; want error without partial flamegraph", result, err)
				}
				if tc.total > maxProfileQueryRecords && !errors.Is(err, ErrInvalidQuery) {
					t.Fatalf("error=%v, want invalid query", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.Flamegraph.Total != int64(tc.total) {
				t.Fatalf("flamegraph total=%d, want %d", result.Flamegraph.Total, tc.total)
			}
		})
	}
}

// Compare equal workloads; the former collect-then-merge strategy retained every
// decoded input profile until the query finished.
func BenchmarkProfileQueryMerge(b *testing.B) {
	for _, collect := range []bool{true, false} {
		name := "batches"
		if collect {
			name = "collect"
		}
		b.Run(name, func(b *testing.B) {
			store := newProfileQueryStore(b, 10001, false)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var merger pprof.ProfileMerge
				var documents []*profilingstore.Document
				err := store.Search(b.Context(), &profilingstore.Filter{Limit: 10001}, func(batch []*profilingstore.Document) error {
					if collect {
						documents = append(documents, batch...)
						return nil
					}
					for _, document := range batch {
						if err := merger.Merge(document.ProfileData.Profile); err != nil {
							return err
						}
					}
					return nil
				})
				if err != nil {
					b.Fatal(err)
				}
				for _, document := range documents {
					if err := merger.Merge(document.ProfileData.Profile); err != nil {
						b.Fatal(err)
					}
				}
				if merger.Profile() == nil {
					b.Fatal("merged profile is missing")
				}
			}
		})
	}
}

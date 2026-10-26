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

	googlev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"

	"github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/types"

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
func newProfileQueryStore(t testing.TB, total int, fail bool, documents ...json.RawMessage) *profilingstore.Store {
	t.Helper()
	var cleared atomic.Bool
	position, size := 0, 0
	document := json.RawMessage(`{"hostname":"node-1","region":"test","uploaded_timestamp":"2026-10-08T00:00:00Z","started_timestamp":"2026-10-08T00:00:00Z","tracer_type":"profiling","tracer_id":"job-1","profile_data":{"profile_type":"process_cpu:cpu:nanoseconds:cpu:nanoseconds","profile":{"string_table":["","cpu","nanoseconds","work"],"sample_type":[{"type":1,"unit":2}],"period_type":{"type":1,"unit":2},"function":[{"id":1,"name":3}],"location":[{"id":1,"line":[{"function_id":1}]}],"sample":[{"location_id":[1],"value":[1]}]}}}`)
	if len(documents) == 0 {
		documents = []json.RawMessage{document}
	}
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
			hits = append(hits, map[string]any{"_id": fmt.Sprint(position), "_source": documents[position%len(documents)]})
			position++
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"_scroll_id": "profiles", "timed_out": false, "_shards": map[string]any{"failed": 0}, "hits": map[string]any{"hits": hits}}); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	store, err := profilingstore.NewFromConfig(context.Background(), profilingstore.Config{Addresses: []string{server.URL}, Index: "profiles"})
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

func TestSelectMergeStacktracesProfileCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name                string
		profileType         string
		legacy              [2]bool
		differentPeriodUnit bool
		emptyPeriod         bool
		selector            string
		declaredType        string
		sampleName          string
		sampleUnit          string
		wantErr             string
	}{
		{name: "periodless CPU", profileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds"},
		{name: "periodless off-CPU", profileType: "process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds"},
		{name: "periodless memory bytes", profileType: "memory:alloc_space:bytes:space:bytes"},
		{name: "periodless memory count", profileType: "memory:alloc_objects:count:space:bytes"},
		{
			name: "legacy CPU", profileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			legacy: [2]bool{true, true},
		},
		{
			name: "legacy then periodless", profileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			legacy: [2]bool{true, false},
		},
		{
			name: "periodless then legacy", profileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			legacy: [2]bool{false, true},
		},
		{
			name: "different period metadata", profileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			legacy: [2]bool{false, true}, differentPeriodUnit: true,
		},
		{
			name: "different legacy periods", profileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			legacy: [2]bool{true, true}, differentPeriodUnit: true,
		},
		{
			name: "mixed memory count windows", profileType: "memory:alloc_objects:count:space:bytes",
			legacy: [2]bool{true, false},
		},
		{
			name: "empty period metadata", profileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			emptyPeriod: true,
		},
		{
			name: "matching selector", profileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			selector: `{id="job-1",__profile_type__="process_cpu:cpu:nanoseconds:cpu:nanoseconds"}`,
		},
		{
			name: "matching repeated selector", profileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			selector: `{id="job-1",__profile_type__="process_cpu:cpu:nanoseconds:cpu:nanoseconds",` +
				`__profile_type__="process_cpu:cpu:nanoseconds:cpu:nanoseconds"}`,
		},
		{
			name: "wrong document type", profileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			declaredType: "process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds", wantErr: "does not match query type",
		},
		{
			name: "wrong sample name", profileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			sampleName: "offcpu", wantErr: "stored profile lacks sample type",
		},
		{
			name: "wrong sample unit", profileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			sampleUnit: "milliseconds", wantErr: "stored profile lacks sample type",
		},
		{
			name:         "physical usage wrong document type",
			profileType:  "memory:physical_usage:bytes:space:bytes",
			declaredType: "memory:alloc_space:bytes:space:bytes",
			wantErr:      "does not match query type",
		},
		{
			name:        "physical usage wrong sample name",
			profileType: "memory:physical_usage:bytes:space:bytes",
			sampleName:  "alloc_space",
			wantErr:     "stored profile lacks sample type",
		},
		{
			name:        "physical usage wrong sample unit",
			profileType: "memory:physical_usage:bytes:space:bytes",
			sampleUnit:  "count",
			wantErr:     "stored profile lacks sample type",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var documents []json.RawMessage
			for i, value := range []int64{7, 11} {
				profile, err := window.BuildProfile(t.Context(), &types.AggregationWindow{
					ProfileType: tc.profileType,
					Aggregated: types.AggregatedProfile{
						TimeNanos: 1, DurationNanos: 100,
						Frames:  []types.ProfileFrame{{Name: "work"}},
						Stacks:  [][]uint32{{0}},
						Samples: []types.AggregatedSample{{UserStack: 1, Value: value}},
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				if tc.legacy[i] {
					parts := strings.Split(tc.profileType, ":")
					if tc.differentPeriodUnit && i == 1 {
						parts[4] = "milliseconds"
					}
					profile.PeriodType = &googlev1.ValueType{
						Type: int64(len(profile.StringTable)),
						Unit: int64(len(profile.StringTable) + 1),
					}
					profile.StringTable = append(profile.StringTable, parts[3], parts[4])
					profile.Period = 10000000
				}
				if tc.emptyPeriod {
					profile.PeriodType = &googlev1.ValueType{}
				}
				declaredType := tc.profileType
				if i == 1 {
					if tc.declaredType != "" {
						declaredType = tc.declaredType
					}
					if tc.sampleName != "" {
						profile.StringTable[profile.SampleType[0].Type] = tc.sampleName
					}
					if tc.sampleUnit != "" {
						profile.StringTable[profile.SampleType[0].Unit] = tc.sampleUnit
					}
				}
				document, err := json.Marshal(map[string]any{
					"hostname": "node-1", "region": "test", "tracer_id": "job-1",
					"uploaded_timestamp": "2026-10-08T00:00:00Z",
					"started_timestamp":  "2026-10-08T00:00:00Z",
					"tracer_type":        "profiling",
					"profile_data":       profilingstore.ProfileData{ProfileType: declaredType, Profile: profile},
				})
				if err != nil {
					t.Fatal(err)
				}
				documents = append(documents, document)
			}
			store := newProfileQueryStore(
				t,
				2,
				false,
				documents...,
			)
			service, err := NewProfileQueryService(store)
			if err != nil {
				t.Fatal(err)
			}
			selector := tc.selector
			if selector == "" {
				selector = `{id="job-1"}`
			}
			result, err := service.SelectMergeStacktraces(t.Context(), &querierv1.SelectMergeStacktracesRequest{
				LabelSelector: selector,
				ProfileTypeID: tc.profileType,
			})
			if tc.wantErr != "" {
				matchesError := err != nil && strings.Contains(err.Error(), tc.wantErr)
				if !matchesError || result != nil {
					t.Errorf("result=%v, error=%v; want %q without partial flamegraph", result, err, tc.wantErr)
				}
				if errors.Is(err, ErrInvalidQuery) {
					t.Errorf("stored data error classified as invalid query: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.GetFlamegraph().GetTotal() != 18 {
				t.Errorf("flamegraph total=%d, want 18 without rescaling", result.GetFlamegraph().GetTotal())
			}
		})
	}
}

func TestSelectMergeStacktracesRejectsConflictingProfileTypes(t *testing.T) {
	const cpuType = "process_cpu:cpu:nanoseconds:cpu:nanoseconds"
	const offCPUType = "process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds"
	for _, tc := range []struct {
		name        string
		profileType string
		selector    string
	}{
		{name: "empty request type", selector: `{id="job-1"}`},
		{
			name: "conflicting selector", profileType: cpuType,
			selector: `{id="job-1",__profile_type__="` + offCPUType + `"}`,
		},
		{
			name: "conflicting repeated selector", profileType: cpuType,
			selector: `{id="job-1",__profile_type__="` + offCPUType + `",__profile_type__="` + cpuType + `"}`,
		},
		{
			name: "empty selector type", profileType: cpuType,
			selector: `{id="job-1",__profile_type__=""}`,
		},
		{
			name: "wildcard selector type", profileType: cpuType,
			selector: `{id="job-1",__profile_type__="*"}`,
		},
		{
			name: "regex selector type", profileType: cpuType,
			selector: `{id="job-1",__profile_type__=~".*"}`,
		},
		{
			name: "negative selector type", profileType: cpuType,
			selector: `{id="job-1",__profile_type__!="` + cpuType + `"}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// An uninitialized store also proves invalid types fail before a search.
			service, err := NewProfileQueryService(&profilingstore.Store{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.SelectMergeStacktraces(t.Context(), &querierv1.SelectMergeStacktracesRequest{
				ProfileTypeID: tc.profileType,
				LabelSelector: tc.selector,
			})
			if !errors.Is(err, ErrInvalidQuery) || result != nil {
				t.Errorf("result=%v, error=%v; want invalid query without a search", result, err)
			}
		})
	}
	for i := range 5 {
		t.Run(fmt.Sprintf("empty component %d", i), func(t *testing.T) {
			parts := strings.Split(cpuType, ":")
			parts[i] = ""
			service, err := NewProfileQueryService(&profilingstore.Store{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.SelectMergeStacktraces(t.Context(), &querierv1.SelectMergeStacktracesRequest{
				ProfileTypeID: strings.Join(parts, ":"),
				LabelSelector: `{id="job-1"}`,
			})
			if !errors.Is(err, ErrInvalidQuery) || result != nil {
				t.Errorf("result=%v, error=%v; want invalid query for empty component %d", result, err, i)
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

func TestSelectMergeStacktracesPhysicalUsageSelectsLatestIncludingZero(t *testing.T) {
	for _, latestValue := range []int64{0, 4096} {
		t.Run(fmt.Sprint(latestValue), func(t *testing.T) {
			document := func(observed, uploaded, value int64) json.RawMessage {
				return json.RawMessage(fmt.Sprintf(`{"hostname":"node-1","uploaded_timestamp":"2026-10-08T00:00:0%dZ","started_timestamp":"2026-10-08T00:00:0%dZ","tracer_type":"profiling","tracer_id":"job-1","profile_data":{"profile_type":"memory:physical_usage:bytes:space:bytes","profile":{"time_nanos":%d,"string_table":["","physical_usage","bytes","work"],"sample_type":[{"type":1,"unit":2}],"function":[{"id":1,"name":3}],"location":[{"id":1,"line":[{"function_id":1}]}],"sample":[{"location_id":[1],"value":[%d]}]}}}`, uploaded, observed, observed*1000000000, value))
			}
			// Delivery order differs from observation order. The latest zero must win.
			store := newProfileQueryStore(t, 3, false,
				document(1, 5, 8192), document(3, 3, latestValue), document(2, 4, 4096))
			service, err := NewProfileQueryService(store)
			if err != nil {
				t.Fatal(err)
			}

			response, err := service.SelectMergeStacktraces(t.Context(), &querierv1.SelectMergeStacktracesRequest{
				LabelSelector: `{id="job-1"}`,
				ProfileTypeID: "memory:physical_usage:bytes:space:bytes",
			})
			if err != nil {
				t.Fatal(err)
			}

			if response.Flamegraph.Total != latestValue {
				t.Fatalf("total=%d, want latest %d", response.Flamegraph.Total, latestValue)
			}
		})
	}
}

func TestSelectMergeStacktracesPhysicalUsageRequiresTask(t *testing.T) {
	service := &ProfileQueryService{}
	for _, selector := range []string{`{hostname="node-1"}`, `{hostname="node-1",__profile_type__="memory:physical_usage:bytes:space:bytes"}`} {
		_, err := service.SelectMergeStacktraces(t.Context(), &querierv1.SelectMergeStacktracesRequest{
			LabelSelector: selector,
			ProfileTypeID: "memory:physical_usage:bytes:space:bytes",
		})
		if !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("error=%v", err)
		}
	}
}

func TestLabelNamesMatchSelectableLabels(t *testing.T) {
	svc := &ProfileQueryService{}
	resp, err := svc.LabelNames(context.Background(), nil)
	if err != nil {
		t.Fatalf("LabelNames() error = %v", err)
	}
	reserved := map[string]bool{"__profile_type__": true}
	for _, name := range resp.Names {
		if reserved[name] {
			t.Fatalf("LabelNames() advertises reserved label %q", name)
		}
		m, err := labels.NewMatcher(labels.MatchEqual, name, "x")
		if err != nil {
			t.Fatalf("NewMatcher(%q): %v", name, err)
		}
		if err := applyProfileMatcher(&profilingstore.Filter{}, m); err != nil {
			t.Fatalf("LabelNames() advertises %q but the matcher rejects it: %v", name, err)
		}
	}
	selectable := []string{"id", "region", "hostname", "container_id", "container_hostname"}
	advertised := map[string]bool{}
	for _, name := range resp.Names {
		advertised[name] = true
	}
	for _, name := range selectable {
		if !advertised[name] {
			t.Fatalf("LabelNames() omits selectable label %q", name)
		}
	}
}

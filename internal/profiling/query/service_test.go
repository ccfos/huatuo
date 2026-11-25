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
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	googlev1 "github.com/grafana/pyroscope/api/gen/proto/go/google/v1"
	querierv1 "github.com/grafana/pyroscope/api/gen/proto/go/querier/v1"
	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	"github.com/grafana/pyroscope/pkg/pprof"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"

	"github.com/ccfos/huatuo/internal/profiling/window"
	"github.com/ccfos/huatuo/pkg/observation"
	profilingstore "github.com/ccfos/huatuo/pkg/profiling/store"
	"github.com/ccfos/huatuo/pkg/types"
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
			result, err := service.SelectMergeStacktraces(context.Background(), &querierv1.SelectMergeStacktracesRequest{LabelSelector: `{scope="host",id="job-1"}`, ProfileTypeID: "process_cpu:cpu:nanoseconds:cpu:nanoseconds"})
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
		{name: "periodless virtual allocation", profileType: "memory:virtual_alloc:bytes:space:bytes"},
		{name: "periodless physical allocation", profileType: "memory:physical_alloc:bytes:space:bytes"},
		{name: "periodless Java object allocation", profileType: "memory:object_alloc:bytes:space:bytes"},
		{name: "periodless Java live objects", profileType: "memory:object_usage:bytes:space:bytes"},
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
			selector: `{scope="host",id="job-1",__profile_type__="process_cpu:cpu:nanoseconds:cpu:nanoseconds"}`,
		},
		{
			name: "matching repeated selector", profileType: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			selector: `{scope="host",id="job-1",__profile_type__="process_cpu:cpu:nanoseconds:cpu:nanoseconds",` +
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
			declaredType: "memory:physical_alloc:bytes:space:bytes",
			wantErr:      "does not match query type",
		},
		{
			name:        "physical usage wrong sample name",
			profileType: "memory:physical_usage:bytes:space:bytes",
			sampleName:  "physical_alloc",
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
						TimeNanos:     1,
						DurationNanos: 100,
						Frames:        []types.ProfileFrame{{Name: "work"}},
						Stacks:        [][]uint32{{0}},
						Samples:       []types.AggregatedSample{{UserStack: 1, Value: value}},
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
				selector = `{scope="host",id="job-1"}`
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
		{name: "empty request type", selector: `{scope="host",id="job-1"}`},
		{
			name: "conflicting selector", profileType: cpuType,
			selector: `{scope="host",id="job-1",__profile_type__="` + offCPUType + `"}`,
		},
		{
			name: "conflicting repeated selector", profileType: cpuType,
			selector: `{scope="host",id="job-1",__profile_type__="` + offCPUType + `",__profile_type__="` + cpuType + `"}`,
		},
		{
			name: "empty selector type", profileType: cpuType,
			selector: `{scope="host",id="job-1",__profile_type__=""}`,
		},
		{
			name: "wildcard selector type", profileType: cpuType,
			selector: `{scope="host",id="job-1",__profile_type__="*"}`,
		},
		{
			name: "regex selector type", profileType: cpuType,
			selector: `{scope="host",id="job-1",__profile_type__=~".*"}`,
		},
		{
			name: "negative selector type", profileType: cpuType,
			selector: `{scope="host",id="job-1",__profile_type__!="` + cpuType + `"}`,
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
				LabelSelector: `{scope="host",id="job-1"}`,
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
				LabelSelector: `{scope="host",id="job-1"}`,
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
	for _, selector := range []string{`{scope="host",hostname="node-1"}`, `{scope="host",hostname="node-1",__profile_type__="memory:physical_usage:bytes:space:bytes"}`} {
		_, err := service.SelectMergeStacktraces(t.Context(), &querierv1.SelectMergeStacktracesRequest{
			LabelSelector: selector,
			ProfileTypeID: "memory:physical_usage:bytes:space:bytes",
		})
		if !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("error=%v", err)
		}
	}
}

func TestSelectMergeStacktracesRejectsInvalidScope(t *testing.T) {
	service := &ProfileQueryService{}
	for _, selector := range []string{
		`{id="job"}`,
		`{scope="other",id="job"}`,
		`{scope="host",scope="container",id="job"}`,
		`{scope="host",container_id="container",id="job"}`,
		`{scope="host",container_namespace="team-a",id="job"}`,
		`{scope="host",container_name="worker",id="job"}`,
		`{scope="host",container_hostname="worker-pod",id="job"}`,
		`{scope="host",container_type="normal",id="job"}`,
		`{scope="host",container_qos="burstable",id="job"}`,
		`{scope="container",hostname="node"}`,
	} {
		t.Run(selector, func(t *testing.T) {
			_, err := service.SelectMergeStacktraces(t.Context(), &querierv1.SelectMergeStacktracesRequest{
				LabelSelector: selector,
				ProfileTypeID: "process_cpu:cpu:nanoseconds:cpu:nanoseconds",
			})
			if !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("error = %v, want invalid query before storage access", err)
			}
		})
	}
}

// equalMatcher builds an equality matcher, the only operator the profile query
// accepts.
func equalMatcher(label, value string) *labels.Matcher {
	return &labels.Matcher{Name: label, Value: value, Type: labels.MatchEqual}
}

func TestProfileMatcherSetAppliesEverySupportedLabel(t *testing.T) {
	tests := []struct {
		name   string
		label  string
		value  string
		stored func(*profilingstore.Filter) string
	}{
		{
			name:   "id",
			label:  "id",
			value:  "tracer-1",
			stored: func(filter *profilingstore.Filter) string { return filter.ID },
		},
		{
			name:   "region",
			label:  "region",
			value:  "cn-beijing",
			stored: func(filter *profilingstore.Filter) string { return filter.Region },
		},
		{
			name:   "hostname",
			label:  "hostname",
			value:  "host-a",
			stored: func(filter *profilingstore.Filter) string { return filter.Hostname },
		},
		{
			name:   "container id",
			label:  "container_id",
			value:  "9f4c2f1a8b7d",
			stored: func(filter *profilingstore.Filter) string { return filter.ContainerID },
		},
		{
			name:   "container name",
			label:  "container_name",
			value:  "worker",
			stored: func(filter *profilingstore.Filter) string { return filter.ContainerName },
		},
		{
			name:   "container type",
			label:  "container_type",
			value:  "normal",
			stored: func(filter *profilingstore.Filter) string { return filter.ContainerType },
		},
		{
			name:   "container qos",
			label:  "container_qos",
			value:  "burstable",
			stored: func(filter *profilingstore.Filter) string { return filter.ContainerQoS },
		},
		{
			name:   "container namespace",
			label:  "container_namespace",
			value:  "team-a",
			stored: func(filter *profilingstore.Filter) string { return filter.ContainerNamespace },
		},
		{
			name:   "container hostname",
			label:  "container_hostname",
			value:  "container-a",
			stored: func(filter *profilingstore.Filter) string { return filter.ContainerHostname },
		},
		{
			name:   "profile type",
			label:  "__profile_type__",
			value:  "cpu:samples:cpu:nanoseconds:nanoseconds",
			stored: func(filter *profilingstore.Filter) string { return filter.ProfileType },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filter := &profilingstore.Filter{}
			matcher := equalMatcher(test.label, test.value)

			if err := newProfileMatcherSet(filter).apply(matcher); err != nil {
				t.Fatalf("apply(%q) error = %v", test.label, err)
			}
			if got := test.stored(filter); got != test.value {
				t.Errorf("filter value after apply(%q) = %q, want %q", test.label, got, test.value)
			}
		})
	}
}

func TestProfileMatcherSetAcceptsRepeatedValues(t *testing.T) {
	filter := &profilingstore.Filter{}
	set := newProfileMatcherSet(filter)

	for i := 0; i < 3; i++ {
		if err := set.apply(equalMatcher("hostname", "host-a")); err != nil {
			t.Fatalf("apply() call %d error = %v, want repeated values to be accepted", i, err)
		}
	}
	if filter.Hostname != "host-a" {
		t.Errorf("filter.Hostname = %q, want %q", filter.Hostname, "host-a")
	}
}

func TestProfileMatcherSetRejectsConflictingValues(t *testing.T) {
	tests := []struct {
		name   string
		label  string
		stored func(*profilingstore.Filter) string
	}{
		{
			name:   "hostname",
			label:  "hostname",
			stored: func(filter *profilingstore.Filter) string { return filter.Hostname },
		},
		{
			name:   "id",
			label:  "id",
			stored: func(filter *profilingstore.Filter) string { return filter.ID },
		},
		{
			name:   "region",
			label:  "region",
			stored: func(filter *profilingstore.Filter) string { return filter.Region },
		},
		{
			name:   "container id",
			label:  "container_id",
			stored: func(filter *profilingstore.Filter) string { return filter.ContainerID },
		},
		{
			name:   "container name",
			label:  "container_name",
			stored: func(filter *profilingstore.Filter) string { return filter.ContainerName },
		},
		{
			name:   "container type",
			label:  "container_type",
			stored: func(filter *profilingstore.Filter) string { return filter.ContainerType },
		},
		{
			name:   "container qos",
			label:  "container_qos",
			stored: func(filter *profilingstore.Filter) string { return filter.ContainerQoS },
		},
		{
			name:   "container namespace",
			label:  "container_namespace",
			stored: func(filter *profilingstore.Filter) string { return filter.ContainerNamespace },
		},
		{
			name:   "container hostname",
			label:  "container_hostname",
			stored: func(filter *profilingstore.Filter) string { return filter.ContainerHostname },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filter := &profilingstore.Filter{}
			set := newProfileMatcherSet(filter)

			if err := set.apply(equalMatcher(test.label, "host-a")); err != nil {
				t.Fatalf("apply(%q) error = %v", test.label, err)
			}

			err := set.apply(equalMatcher(test.label, "host-b"))
			if err == nil {
				t.Fatalf("apply(%q) error = nil, want conflicting values to be rejected", test.label)
			}
			if !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("apply(%q) error = %v, want ErrInvalidQuery", test.label, err)
			}
			for _, value := range []string{"host-a", "host-b"} {
				if !strings.Contains(err.Error(), value) {
					t.Errorf("apply() error = %v, want it to mention %q", err, value)
				}
			}
			if got := test.stored(filter); got != "host-a" {
				t.Errorf("filter value = %q, want the first matcher value %q", got, "host-a")
			}
		})
	}
}

// The conflict must be reported for either matcher order, because the caller
// controls the order of the parsed selector.
func TestProfileMatcherSetConflictDoesNotDependOnOrder(t *testing.T) {
	for _, values := range [][2]string{{"host-a", "host-b"}, {"host-b", "host-a"}} {
		filter := &profilingstore.Filter{}
		set := newProfileMatcherSet(filter)

		if err := set.apply(equalMatcher("hostname", values[0])); err != nil {
			t.Fatalf("apply(%q) error = %v", values[0], err)
		}

		err := set.apply(equalMatcher("hostname", values[1]))
		if !errors.Is(err, ErrInvalidQuery) {
			t.Fatalf("apply(%q) after %q error = %v, want ErrInvalidQuery", values[1], values[0], err)
		}
		if filter.Hostname != values[0] {
			t.Errorf("filter.Hostname = %q, want the first matcher value %q", filter.Hostname, values[0])
		}
	}
}

func TestProfileMatcherSetRejectsUnsupportedOperator(t *testing.T) {
	filter := &profilingstore.Filter{}
	matcher := &labels.Matcher{Name: "hostname", Value: "host-.*", Type: labels.MatchRegexp}

	err := newProfileMatcherSet(filter).apply(matcher)
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("apply() error = %v, want ErrInvalidQuery", err)
	}
	if !strings.Contains(err.Error(), "only supports equality") {
		t.Errorf("apply() error = %v, want an equality-only error", err)
	}
	if filter.Hostname != "" {
		t.Errorf("filter.Hostname = %q, want the filter to stay empty", filter.Hostname)
	}
}

func TestProfileMatcherSetRejectsUnknownLabel(t *testing.T) {
	filter := &profilingstore.Filter{}

	err := newProfileMatcherSet(filter).apply(equalMatcher("unknown", "x"))
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("apply() error = %v, want ErrInvalidQuery", err)
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("apply() error = %v, want it to mention the rejected label", err)
	}
}

// newQueryServiceWithoutStorage builds a query service whose storage is never
// reached: request validation rejects the conflicting selector before the store
// is queried, so these tests need no Elasticsearch.
func newQueryServiceWithoutStorage(t *testing.T) *ProfileQueryService {
	t.Helper()

	service, err := NewProfileQueryService(&profilingstore.Store{})
	if err != nil {
		t.Fatalf("NewProfileQueryService() error = %v", err)
	}
	return service
}

func TestSelectMergeStacktracesRejectsConflictingMatchers(t *testing.T) {
	service := newQueryServiceWithoutStorage(t)

	_, err := service.SelectMergeStacktraces(t.Context(), &querierv1.SelectMergeStacktracesRequest{
		ProfileTypeID: "cpu:samples:cpu:nanoseconds:nanoseconds",
		LabelSelector: `{hostname="host-a",hostname="host-b"}`,
		Start:         1_000,
		End:           2_000,
	})
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("SelectMergeStacktraces() error = %v, want ErrInvalidQuery", err)
	}
	for _, value := range []string{"host-a", "host-b"} {
		if !strings.Contains(err.Error(), value) {
			t.Errorf("SelectMergeStacktraces() error = %v, want it to mention %q", err, value)
		}
	}
}

func TestResolveProfileMatchersAcceptsRepeatedValues(t *testing.T) {
	filter := &profilingstore.Filter{}
	matchers, err := parser.ParseMetricSelector(`{scope="host",hostname="host-a",hostname="host-a"}`)
	if err != nil {
		t.Fatalf("ParseMetricSelector() error = %v", err)
	}

	if err := resolveProfileMatchers(filter, matchers, "cpu:samples:cpu:nanoseconds:nanoseconds"); err != nil {
		t.Fatalf("resolveProfileMatchers() error = %v, want repeated values to be accepted", err)
	}
	if filter.Hostname != "host-a" {
		t.Errorf("Hostname = %q, want %q", filter.Hostname, "host-a")
	}
	if filter.Scope != observation.ScopeTypeHost {
		t.Errorf("Scope = %v, want %v", filter.Scope, observation.ScopeTypeHost)
	}
}

// A wildcard value means "all values" and only drops its own restriction, so
// it must not turn a later matcher for the same label into a conflict.
func TestResolveProfileMatchersSkipsWildcardValue(t *testing.T) {
	filter := &profilingstore.Filter{}
	matchers, err := parser.ParseMetricSelector(`{scope="host",hostname="*",hostname="host-b"}`)
	if err != nil {
		t.Fatalf("ParseMetricSelector() error = %v", err)
	}

	if err := resolveProfileMatchers(filter, matchers, "cpu:samples:cpu:nanoseconds:nanoseconds"); err != nil {
		t.Fatalf("resolveProfileMatchers() error = %v, want the wildcard matcher to be skipped", err)
	}
	if filter.Hostname != "host-b" {
		t.Errorf("Hostname = %q, want %q", filter.Hostname, "host-b")
	}
}

// Non-equality operators must be rejected even when the value happens to be
// the wildcard "*", which the wildcard skip would otherwise swallow.
func TestResolveProfileMatchersRejectsNonEqualityMatcher(t *testing.T) {
	for _, selector := range []string{
		`{scope="host",hostname!="*"}`,
		`{scope="host",hostname=~"host-.*"}`,
	} {
		t.Run(selector, func(t *testing.T) {
			filter := &profilingstore.Filter{}
			matchers, err := parser.ParseMetricSelector(selector)
			if err != nil {
				t.Fatalf("ParseMetricSelector() error = %v", err)
			}

			err = resolveProfileMatchers(filter, matchers, "cpu:samples:cpu:nanoseconds:nanoseconds")
			if !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("resolveProfileMatchers() error = %v, want ErrInvalidQuery", err)
			}
			if !strings.Contains(err.Error(), "only supports equality") {
				t.Errorf("resolveProfileMatchers() error = %v, want an equality-only error", err)
			}
		})
	}
}

// LabelValues feeds the same single-value aggregation filter, so a conflicting
// selector must be rejected there as well.
func TestLabelValuesRejectsConflictingMatchers(t *testing.T) {
	service := newQueryServiceWithoutStorage(t)

	_, err := service.LabelValues(t.Context(), &typesv1.LabelValuesRequest{
		Name: "hostname",
		Matchers: []string{
			`{__profile_type__="cpu:samples:cpu:nanoseconds:nanoseconds",hostname="host-a",hostname="host-b"}`,
		},
		Start: 1_000,
		End:   2_000,
	})
	if !errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("LabelValues() error = %v, want ErrInvalidQuery", err)
	}
	if !strings.Contains(err.Error(), "hostname") {
		t.Errorf("LabelValues() error = %v, want it to mention the conflicting label", err)
	}
}

func TestLabelNamesIncludesPublicMetadata(t *testing.T) {
	service := newQueryServiceWithoutStorage(t)
	response, err := service.LabelNames(t.Context(), &typesv1.LabelNamesRequest{})
	if err != nil {
		t.Fatal(err)
	}

	for _, field := range []string{
		"region", "hostname", "container_id", "container_name",
		"container_hostname", "container_namespace", "container_type", "container_qos",
	} {
		if !slices.Contains(response.Names, field) {
			t.Errorf("label names = %v, missing public metadata %q", response.Names, field)
		}
	}
}

func TestLabelValuesRejectsHostScopeWithContainerMetadata(t *testing.T) {
	service := newQueryServiceWithoutStorage(t)
	for _, field := range []string{
		"container_id", "container_name", "container_hostname",
		"container_namespace", "container_type", "container_qos",
	} {
		t.Run(field, func(t *testing.T) {
			_, err := service.LabelValues(t.Context(), &typesv1.LabelValuesRequest{
				Name: "hostname",
				Matchers: []string{
					fmt.Sprintf(`{__profile_type__="process_cpu:cpu:nanoseconds:cpu:nanoseconds",scope="host",%s="worker"}`, field),
				},
			})
			if !errors.Is(err, ErrInvalidQuery) || !strings.Contains(err.Error(), "host scope") {
				t.Fatalf("LabelValues() error = %v, want host scope conflict before storage access", err)
			}
		})
	}
}

func labelQueryDocument(t testing.TB, profileType string, observed int64) json.RawMessage {
	t.Helper()
	profile := &googlev1.Profile{
		TimeNanos:   observed,
		StringTable: []string{"", "cpu", "nanoseconds", "work", "pid", "123", "456", "789", "comm", "worker", "all", "cpu", "0", "1", "custom", "a+b", "size", "bytes", "physical_usage", "role", "reader", "writer"},
		SampleType: []*googlev1.ValueType{{
			Type: 1,
			Unit: 2,
		}},
		Function: []*googlev1.Function{{
			Id:   1,
			Name: 3,
		}},
		Location: []*googlev1.Location{{
			Id:   1,
			Line: []*googlev1.Line{{FunctionId: 1}},
		}},
		Sample: []*googlev1.Sample{
			{
				LocationId: []uint64{1},
				Value:      []int64{7},
				Label: []*googlev1.Label{{
					Key: 4,
					Str: 5,
				}, {
					Key: 8,
					Str: 9,
				}, {
					Key: 11,
					Str: 12,
				}, {
					Key: 14,
					Str: 15,
				}, {
					Key:     16,
					Num:     4096,
					NumUnit: 17,
				}, {
					Key: 19,
					Str: 20,
				}, {
					Key: 19,
					Str: 21,
				}},
			},
			{
				LocationId: []uint64{1},
				Value:      []int64{11},
				Label: []*googlev1.Label{{
					Key: 4,
					Str: 6,
				}, {
					Key: 8,
					Str: 10,
				}, {
					Key: 11,
					Str: 13,
				}, {
					Key:     16,
					Num:     8192,
					NumUnit: 17,
				}},
			},
			{
				LocationId: []uint64{1},
				Value:      []int64{13},
				Label: []*googlev1.Label{{
					Key: 4,
					Str: 7,
				}, {
					Key: 8,
					Str: 9,
				}},
			},
		},
	}
	if profileType == "memory:physical_usage:bytes:space:bytes" {
		profile.SampleType = []*googlev1.ValueType{{
			Type: 18,
			Unit: 17,
		}}
		if observed > 1 {
			profile.Sample = profile.Sample[1:]
		}
	}
	data, err := json.Marshal(map[string]any{
		"hostname": "node-1", "region": "test", "tracer_id": "job-1",
		"uploaded_timestamp": "2026-10-08T00:00:00Z", "started_timestamp": "2026-10-08T00:00:00Z", "tracer_type": "profiling",
		"profile_data": profilingstore.ProfileData{
			ProfileType: profileType,
			Profile:     profile,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	return data
}

func TestProfileQuerySampleLabels(t *testing.T) {
	const profileType = "process_cpu:cpu:nanoseconds:cpu:nanoseconds"
	for _, tc := range []struct {
		name, selector string
		total          int64
	}{
		{
			"unfiltered",
			"",
			31,
		},
		{
			"pid",
			`,pid="123"`,
			7,
		},
		{
			"comm",
			`,comm="worker"`,
			20,
		},
		{
			"cpu zero",
			`,cpu="0"`,
			7,
		},
		{
			"all includes missing cpu",
			`,cpu=~".*"`,
			31,
		},
		{
			"literal all",
			`,comm="all"`,
			11,
		},
		{
			"new label",
			`,custom="a+b"`,
			7,
		},
		{
			"escaped regexp",
			`,custom=~"a\\+b"`,
			7,
		},
		{
			"numeric with unit",
			`,size="4096 bytes"`,
			7,
		},
		{
			"and",
			`,comm="worker",cpu="0"`,
			7,
		},
		{
			"unknown label",
			`,future_label="value"`,
			0,
		},
		{
			"multi value",
			`,role="writer"`,
			7,
		},
		{
			"multiple values of one label",
			`,role="reader",role="writer"`,
			7,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newProfileQueryStore(t, 1, false, labelQueryDocument(t, profileType, 1))
			service, err := NewProfileQueryService(store)
			if err != nil {
				t.Fatal(err)
			}
			response, err := service.SelectMergeStacktraces(t.Context(), &querierv1.SelectMergeStacktracesRequest{
				ProfileTypeID: profileType,
				LabelSelector: `{scope="host",id="job-1"` + tc.selector + `}`,
			})
			if err != nil {
				t.Fatal(err)
			}
			if response.GetFlamegraph().GetTotal() != tc.total {
				t.Fatalf("total=%d, want %d", response.GetFlamegraph().GetTotal(), tc.total)
			}
		})
	}
}

func TestProfileQuerySampleLabelDiscovery(t *testing.T) {
	const profileType = "process_cpu:cpu:nanoseconds:cpu:nanoseconds"
	store := newProfileQueryStore(t, 2, false, labelQueryDocument(t, profileType, 1))
	service, err := NewProfileQueryService(store)
	if err != nil {
		t.Fatal(err)
	}
	base := `{__profile_type__="` + profileType + `",id="job-1"`
	for _, tc := range []struct {
		name, label, selector string
		want                  []string
	}{
		{
			"pid",
			"pid",
			"",
			[]string{"123", "456", "789"},
		},
		{
			"comm filters pid",
			"pid",
			`,comm="worker"`,
			[]string{"123", "789"},
		},
		{
			"numeric label",
			"size",
			"",
			[]string{"4096 bytes", "8192 bytes"},
		},
		{
			"new label",
			"custom",
			"",
			[]string{"a+b"},
		},
		{
			"multi label",
			"role",
			"",
			[]string{"reader", "writer"},
		},
		{
			"unknown",
			"future",
			"",
			[]string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := service.LabelValues(t.Context(), &typesv1.LabelValuesRequest{
				Name:     tc.label,
				Matchers: []string{base + tc.selector + `}`},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(response.Names, tc.want) {
				t.Fatalf("values=%v, want %v", response.Names, tc.want)
			}
		})
	}

	response, err := service.LabelNames(t.Context(), &typesv1.LabelNamesRequest{Matchers: []string{base + `,pid="123"}`}})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"pid", "cpu", "comm", "custom", "size", "role"} {
		if !slices.Contains(response.Names, name) {
			t.Errorf("names=%v, missing %q", response.Names, name)
		}
	}
}

func TestProfileQueryOffCPUReasons(t *testing.T) {
	const profileType = "process_offcpu:offcpu:nanoseconds:offcpu:nanoseconds"
	var document profilingstore.Document
	if err := json.Unmarshal(labelQueryDocument(t, profileType, 1), &document); err != nil {
		t.Fatal(err)
	}

	profile := document.ProfileData.Profile
	profile.StringTable[1] = "offcpu"
	key := int64(len(profile.StringTable))
	profile.StringTable = append(
		profile.StringTable,
		"offcpu_reason",
		"blocked",
		"runqueue",
		"runqueue_preempted",
	)
	for i, sample := range profile.Sample {
		sample.Label = append(sample.Label, &googlev1.Label{
			Key: key,
			Str: key + int64(i) + 1,
		})
	}

	data, err := json.Marshal(&document)
	if err != nil {
		t.Fatal(err)
	}

	store := newProfileQueryStore(
		t,
		1,
		false,
		data,
	)
	service, err := NewProfileQueryService(store)
	if err != nil {
		t.Fatal(err)
	}

	base := `{__profile_type__="` + profileType + `",scope="host",id="job-1"`
	names, err := service.LabelNames(t.Context(), &typesv1.LabelNamesRequest{Matchers: []string{base + `}`}})
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"pid", "comm", "cpu", "offcpu_reason"} {
		if !slices.Contains(names.Names, name) {
			t.Fatalf("label names = %v, missing %s", names.Names, name)
		}
	}

	values, err := service.LabelValues(t.Context(), &typesv1.LabelValuesRequest{
		Name:     "offcpu_reason",
		Matchers: []string{base + `}`},
	})
	if err != nil {
		t.Fatal(err)
	}

	if want := []string{"blocked", "runqueue", "runqueue_preempted"}; !slices.Equal(values.Names, want) {
		t.Fatalf("off-CPU reasons = %v, want %v", values.Names, want)
	}

	for _, tc := range []struct {
		name, selector string
		total          int64
	}{
		{
			name:  "all",
			total: 31,
		},
		{
			name:     "blocked",
			selector: `,offcpu_reason="blocked"`,
			total:    7,
		},
		{
			name:     "runqueue",
			selector: `,offcpu_reason="runqueue"`,
			total:    11,
		},
		{
			name:     "runqueue regexp",
			selector: `,offcpu_reason=~"runqueue.*"`,
			total:    24,
		},
		{
			name:     "CPU zero excludes missing CPU",
			selector: `,cpu="0"`,
			total:    7,
		},
		{
			name:     "CPU regexp excludes missing CPU",
			selector: `,cpu=~"0|1"`,
			total:    18,
		},
		{
			name:     "CPU regexp includes missing CPU",
			selector: `,cpu=~".*"`,
			total:    31,
		},
		{
			name:     "combined process CPU and reason",
			selector: `,pid="123",comm="worker",cpu="0",offcpu_reason="blocked"`,
			total:    7,
		},
		{
			name:     "absent",
			selector: `,offcpu_reason="unknown"`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := service.SelectMergeStacktraces(t.Context(), &querierv1.SelectMergeStacktracesRequest{
				ProfileTypeID: profileType,
				LabelSelector: base + tc.selector + `}`,
			})
			if err != nil {
				t.Fatal(err)
			}

			if got := response.GetFlamegraph().GetTotal(); got != tc.total {
				t.Fatalf("off-CPU nanoseconds = %d, want %d", got, tc.total)
			}
		})
	}
}

func TestProfileQuerySampleLabelsUseLatestSnapshot(t *testing.T) {
	const profileType = "memory:physical_usage:bytes:space:bytes"
	store := newProfileQueryStore(t, 2, false, labelQueryDocument(t, profileType, 1), labelQueryDocument(t, profileType, 2))
	service, err := NewProfileQueryService(store)
	if err != nil {
		t.Fatal(err)
	}
	response, err := service.SelectMergeStacktraces(t.Context(), &querierv1.SelectMergeStacktracesRequest{
		ProfileTypeID: profileType,
		LabelSelector: `{scope="host",id="job-1",pid="123"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.GetFlamegraph().GetTotal() != 0 {
		t.Fatalf("total=%d, want no samples in latest snapshot", response.GetFlamegraph().GetTotal())
	}
	values, err := service.LabelValues(t.Context(), &typesv1.LabelValuesRequest{
		Name:     "pid",
		Matchers: []string{`{__profile_type__="` + profileType + `",id="job-1"}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(values.Names, []string{"456", "789"}) {
		t.Fatalf("values=%v, want only latest snapshot PIDs", values.Names)
	}
}

func TestProfileQueryRejectsInvalidSampleLabels(t *testing.T) {
	for _, expression := range []string{`{scope="host",id="job-1",__future__="x"}`, `{scope="host",id="job-1",pid!="123"}`} {
		if _, err := parseProfileSelector(expression, "process_cpu:cpu:nanoseconds:cpu:nanoseconds"); !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("selector=%s, error=%v, want invalid query", expression, err)
		}
	}

	profile := &googlev1.Profile{StringTable: []string{"", "pid"}}
	for _, label := range []*googlev1.Label{{
		Key: 1,
		Str: 2,
	}, {
		Key:     1,
		NumUnit: 2,
	}} {
		if _, err := profileLabelValue(profile, label); err == nil {
			t.Errorf("label=%v, want corrupt string table error", label)
		}
	}
}

func BenchmarkMatchSampleLabels(b *testing.B) {
	var document profilingstore.Document
	if err := json.Unmarshal(labelQueryDocument(b, "process_cpu:cpu:nanoseconds:cpu:nanoseconds", 1), &document); err != nil {
		b.Fatal(err)
	}
	for _, expression := range []string{`{pid="123",cpu="0"}`, `{comm=~"work.*",cpu=~".*"}`} {
		b.Run(expression, func(b *testing.B) {
			selector, err := parseProfileSelector(expression, "")
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				matched, err := matchSampleLabels(document.ProfileData.Profile, document.ProfileData.Profile.Sample[0], selector.sampleMatchers)
				if err != nil || !matched {
					b.Fatalf("matched=%v,error=%v", matched, err)
				}
			}
		})
	}
}

func TestProfileQueryFiltersNumericLabelsBeforeMerge(t *testing.T) {
	const profileType = "process_cpu:cpu:nanoseconds:cpu:nanoseconds"
	var document profilingstore.Document
	if err := json.Unmarshal(labelQueryDocument(t, profileType, 1), &document); err != nil {
		t.Fatal(err)
	}
	profile := document.ProfileData.Profile
	profile.Sample = profile.Sample[:2]
	// These samples differ only in numeric label values, which the merger ignores.
	profile.Sample[0].Label = []*googlev1.Label{{
		Key:     16,
		Num:     4096,
		NumUnit: 17,
	}}
	profile.Sample[1].Label = []*googlev1.Label{{
		Key:     16,
		Num:     8192,
		NumUnit: 17,
	}}
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	store := newProfileQueryStore(t, 1, false, data)
	service, err := NewProfileQueryService(store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.SelectMergeStacktraces(t.Context(), &querierv1.SelectMergeStacktracesRequest{
		ProfileTypeID: profileType,
		LabelSelector: `{scope="host",id="job-1",size="4096 bytes"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.GetFlamegraph().GetTotal() != 7 {
		t.Fatalf("total=%d, want 7 before numeric labels collapse", result.GetFlamegraph().GetTotal())
	}
}

func TestProfileQueryLabelValueLimit(t *testing.T) {
	const profileType = "process_cpu:cpu:nanoseconds:cpu:nanoseconds"
	var document profilingstore.Document
	if err := json.Unmarshal(labelQueryDocument(t, profileType, 1), &document); err != nil {
		t.Fatal(err)
	}
	profile := document.ProfileData.Profile
	profile.Sample = nil
	for i := 0; i <= maxProfileLabelValues; i++ {
		index := int64(len(profile.StringTable))
		profile.StringTable = append(profile.StringTable, fmt.Sprint(i))
		profile.Sample = append(profile.Sample, &googlev1.Sample{
			LocationId: []uint64{1},
			Value:      []int64{1},
			Label: []*googlev1.Label{{
				Key: 4,
				Str: index,
			}},
		})
	}

	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	store := newProfileQueryStore(t, 1, false, data)
	service, err := NewProfileQueryService(store)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.LabelValues(t.Context(), &typesv1.LabelValuesRequest{
		Name:     "pid",
		Matchers: []string{`{__profile_type__="` + profileType + `",id="job-1"}`},
	})
	if !errors.Is(err, ErrInvalidQuery) || result != nil {
		t.Fatalf("result=%v, error=%v; want no partial label value list", result, err)
	}
}

func TestProfileQueryDocumentLabelValues(t *testing.T) {
	var field atomic.Value
	field.Store("")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`{"version":{"number":"8.15.5"}}`))
			return
		}

		var request struct {
			Aggregations map[string]struct {
				Terms struct {
					Field string `json:"field"`
				} `json:"terms"`
			} `json:"aggregations"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}

		indexedField := request.Aggregations["terms"].Terms.Field
		field.Store(indexedField)
		if indexedField == "" {
			http.Error(w, "expected indexed aggregation", http.StatusBadRequest)
			return
		}

		_, _ = w.Write([]byte(`{"timed_out":false,"_shards":{"failed":0},"hits":{"hits":[]},"aggregations":{"sterms#terms":{"buckets":[{"key":"indexed-value","doc_count":1}]}}}`))
	}))
	t.Cleanup(server.Close)
	store, err := profilingstore.NewFromConfig(t.Context(), profilingstore.Config{
		Addresses: []string{server.URL},
		Index:     "profiles",
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		if err := store.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	service, err := NewProfileQueryService(store)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		"id", "region", "hostname", "container_id", "container_name", "container_hostname", "container_namespace",
		"profile_type", "container_type", "container_qos", "tracer_name", "tracer_id", "tracer_type",
	} {
		t.Run(name, func(t *testing.T) {
			response, err := service.LabelValues(t.Context(), &typesv1.LabelValuesRequest{
				Name:     name,
				Matchers: []string{`{__profile_type__="process_cpu:cpu:nanoseconds:cpu:nanoseconds",id="job-1"}`},
			})
			if err != nil {
				t.Fatal(err)
			}

			if !slices.Equal(response.Names, []string{"indexed-value"}) {
				t.Errorf("values=%v, want indexed values", response.Names)
			}

			wantField := name + ".keyword"
			switch name {
			case "id":
				wantField = "tracer_id.keyword"
			case "profile_type":
				wantField = "profile_data.profile_type.keyword"
			}
			if field.Load() != wantField {
				t.Errorf("aggregation field=%v, want %s", field.Load(), wantField)
			}
		})
	}
}

func TestProfileSelectorPublicMetadata(t *testing.T) {
	for _, field := range documentLabelNames[2:] {
		t.Run(field, func(t *testing.T) {
			selector, err := parseProfileSelector(fmt.Sprintf(`{%s="value"}`, field), "")
			if err != nil {
				t.Fatal(err)
			}
			if len(selector.sampleMatchers) != 0 {
				t.Fatalf("public metadata %q classified as sample label", field)
			}
			_, err = parseProfileSelector(fmt.Sprintf(`{%s=~"value.*"}`, field), "")
			if !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("public metadata regexp error = %v, want invalid query", err)
			}
		})
	}
}

func TestLabelNamesRejectsHostScopeWithContainerMetadata(t *testing.T) {
	service := newQueryServiceWithoutStorage(t)
	for _, field := range []string{
		"container_id", "container_name", "container_hostname",
		"container_namespace", "container_type", "container_qos",
	} {
		t.Run(field, func(t *testing.T) {
			_, err := service.LabelNames(t.Context(), &typesv1.LabelNamesRequest{
				Matchers: []string{fmt.Sprintf(`{scope="host",%s="worker"}`, field)},
			})
			if !errors.Is(err, ErrInvalidQuery) || !strings.Contains(err.Error(), "host scope") {
				t.Fatalf("LabelNames() error = %v, want host scope conflict before storage access", err)
			}
		})
	}
}

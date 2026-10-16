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

	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
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

func TestSelectMergeStacktracesAcceptsRepeatedMatcherValues(t *testing.T) {
	service := newQueryServiceWithoutStorage(t)

	_, err := service.SelectMergeStacktraces(t.Context(), &querierv1.SelectMergeStacktracesRequest{
		ProfileTypeID: "cpu:samples:cpu:nanoseconds:nanoseconds",
		LabelSelector: `{hostname="host-a",hostname="host-a"}`,
		Start:         1_000,
		End:           2_000,
	})
	if errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("SelectMergeStacktraces() error = %v, want repeated values to be accepted", err)
	}
}

// A wildcard value means "all values" and is skipped before the conflict check,
// so it must not turn the next matcher for the same label into a conflict.
func TestSelectMergeStacktracesSkipsWildcardValue(t *testing.T) {
	service := newQueryServiceWithoutStorage(t)

	_, err := service.SelectMergeStacktraces(t.Context(), &querierv1.SelectMergeStacktracesRequest{
		ProfileTypeID: "cpu:samples:cpu:nanoseconds:nanoseconds",
		LabelSelector: `{hostname="*",hostname="host-b"}`,
		Start:         1_000,
		End:           2_000,
	})
	if errors.Is(err, ErrInvalidQuery) {
		t.Fatalf("SelectMergeStacktraces() error = %v, want the wildcard matcher to be skipped", err)
	}
}

// Non-equality operators must be rejected even when the value happens to be
// the wildcard "*", which the validation loop would otherwise skip.
func TestSelectMergeStacktracesRejectsNonEqualityMatcher(t *testing.T) {
	service := newQueryServiceWithoutStorage(t)

	for _, selector := range []string{
		`{hostname!="*"}`,
		`{hostname=~"host-.*"}`,
	} {
		t.Run(selector, func(t *testing.T) {
			_, err := service.SelectMergeStacktraces(t.Context(), &querierv1.SelectMergeStacktracesRequest{
				ProfileTypeID: "cpu:samples:cpu:nanoseconds:nanoseconds",
				LabelSelector: selector,
				Start:         1_000,
				End:           2_000,
			})
			if !errors.Is(err, ErrInvalidQuery) {
				t.Fatalf("SelectMergeStacktraces() error = %v, want ErrInvalidQuery", err)
			}
			if !strings.Contains(err.Error(), "only supports equality") {
				t.Errorf("SelectMergeStacktraces() error = %v, want an equality-only error", err)
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

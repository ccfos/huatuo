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

package service

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"huatuo-bamai/internal/profiler"

	typesv1 "github.com/grafana/pyroscope/api/gen/proto/go/types/v1"
	"github.com/prometheus/prometheus/model/labels"
)

type labelValuesStorage struct {
	aggregationField string
}

func (*labelValuesStorage) Close(context.Context) error { return nil }
func (*labelValuesStorage) Ready(context.Context) error { return nil }
func (*labelValuesStorage) SearchProfilesContext(
	context.Context,
	*SearchFilter,
) ([]*ProfileDocument, error) {
	return nil, nil
}

func (*labelValuesStorage) SearchProfilesPageContext(
	context.Context,
	*SearchFilter,
	[]any,
) ([]*ProfileDocument, []any, error) {
	return nil, nil, nil
}

func (*labelValuesStorage) CountProfilesContext(
	context.Context,
	*SearchFilter,
) (int64, error) {
	return 0, nil
}

func (s *labelValuesStorage) AggregationsByFieldContext(
	_ context.Context,
	_ *SearchFilter,
	field string,
) ([]string, error) {
	s.aggregationField = field
	return []string{"profile-a"}, nil
}

func TestServiceReadyRejectsUninitializedStorage(t *testing.T) {
	err := (*Service)(nil).Ready(t.Context())
	if err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("Ready() error = %v, want initialization error", err)
	}
}

func TestApplyProfileMatcherRegion(t *testing.T) {
	filter := &SearchFilter{}
	matcher := &labels.Matcher{Name: "region", Value: "cn-beijing", Type: labels.MatchEqual}

	if err := applyProfileMatcher(filter, matcher); err != nil {
		t.Fatalf("applyProfileMatcher() error = %v", err)
	}
	if filter.Region != "cn-beijing" {
		t.Errorf("filter.Region = %q, want %q", filter.Region, "cn-beijing")
	}
}

func TestApplyProfileMatcherRejectsUnknownLabel(t *testing.T) {
	filter := &SearchFilter{}
	matcher := &labels.Matcher{Name: "unknown", Value: "x", Type: labels.MatchEqual}

	if err := applyProfileMatcher(filter, matcher); err == nil {
		t.Fatal("applyProfileMatcher() error = nil, want error for unknown label")
	}
}

func TestApplyProfileMatcherSkipsDashboardAllSentinel(t *testing.T) {
	filter := &SearchFilter{}
	matcher := &labels.Matcher{
		Name:  profiler.LabelCPU,
		Value: ProfileAllValue,
		Type:  labels.MatchEqual,
	}

	if err := applyProfileMatcher(filter, matcher); err != nil {
		t.Fatalf("applyProfileMatcher() error = %v", err)
	}
	if len(filter.Labels) != 0 {
		t.Fatalf("filter labels = %#v, want empty", filter.Labels)
	}
}

func TestLabelNamesIncludesProfileIdentifierAliases(t *testing.T) {
	response, err := (&Service{}).LabelNames(
		t.Context(),
		&typesv1.LabelNamesRequest{},
	)
	if err != nil {
		t.Fatalf("LabelNames() error = %v", err)
	}

	want := []string{
		profiler.LabelProfileID,
		profiler.LabelTracer,
		"region",
		"hostname",
		"container_id",
		"container_hostname",
		"container_host_namespace",
		profiler.LabelProfilingScope,
		profiler.LabelCPU,
		profiler.LabelPID,
		profiler.LabelTGID,
	}
	if !reflect.DeepEqual(response.Names, want) {
		t.Fatalf("LabelNames() = %v, want %v", response.Names, want)
	}
}

func TestLabelValuesAcceptsProfileIdentifierAliases(t *testing.T) {
	for _, name := range profiler.ProfileIdentifierLabelNames() {
		t.Run(name, func(t *testing.T) {
			storage := &labelValuesStorage{}
			service := &Service{profileStorage: storage}
			response, err := service.LabelValues(t.Context(), &typesv1.LabelValuesRequest{
				Name: name,
				Matchers: []string{
					`{__profile_type__="process_cpu:cpu:nanoseconds:cpu:nanoseconds"}`,
				},
			})
			if err != nil {
				t.Fatalf("LabelValues(%q) error = %v", name, err)
			}
			if storage.aggregationField != name {
				t.Fatalf(
					"aggregation field = %q, want %q",
					storage.aggregationField,
					name,
				)
			}
			if !reflect.DeepEqual(response.Names, []string{"profile-a"}) {
				t.Fatalf("LabelValues(%q) = %v", name, response.Names)
			}
		})
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

func TestBuildProfileSearchQueryIncludesPage(t *testing.T) {
	query := buildProfileSearchQuery(&SearchFilter{TracerID: "task-2026", Limit: 25, Offset: 50})
	if query.Limit != 25 || query.Offset != 50 {
		t.Fatalf("query page=(%d,%d), want (25,50)", query.Limit, query.Offset)
	}
}

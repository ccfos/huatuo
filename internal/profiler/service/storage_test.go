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
	"testing"

	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/storage/driver"
)

func TestProfileDocumentMapperExposesCollectionDimensionFields(t *testing.T) {
	document := &ProfileDocument{}
	document.TracerData.Flamedata.Labels = map[string]string{
		profiler.LabelProfilingScope: "tgid",
		profiler.LabelTGID:           "4242",
		"custom_label":               "not-queryable-through-legacy-api",
	}

	fields, err := (profileDocumentMapper{}).Fields(document)
	if err != nil {
		t.Fatalf("Fields() error = %v", err)
	}
	if got := fields[profileLabelField(profiler.LabelProfilingScope)]; got != "tgid" {
		t.Fatalf("profiling_scope field = %v", got)
	}
	if got := fields[profileLabelField(profiler.LabelTGID)]; got != "4242" {
		t.Fatalf("tgid field = %v", got)
	}
	if _, ok := fields[profileLabelField("custom_label")]; ok {
		t.Fatal("custom label unexpectedly exposed as a legacy query field")
	}
}

func TestProfileDimensionQueriesUseKeywordFields(t *testing.T) {
	field, err := normalizeProfileAggregationField(profiler.LabelCgroupPath)
	if err != nil {
		t.Fatalf("normalizeProfileAggregationField() error = %v", err)
	}
	wantField := profileLabelKeywordField(profiler.LabelCgroupPath)
	if field != wantField {
		t.Fatalf("aggregation field = %q, want %q", field, wantField)
	}

	query := buildProfileAggregationQuery(&SearchFilter{Labels: map[string]string{
		profiler.LabelTGID:       "42",
		profiler.LabelCgroupPath: "/workload.slice",
		"unknown":                "ignored",
	}})
	want := []driver.Filter{
		{Field: profileLabelKeywordField(profiler.LabelTGID), Op: driver.OpEq, Value: "42"},
		{Field: profileLabelKeywordField(profiler.LabelCgroupPath), Op: driver.OpEq, Value: "/workload.slice"},
	}
	if len(query.Filters) != len(want) {
		t.Fatalf("filters = %#v, want %#v", query.Filters, want)
	}
	for i := range want {
		if query.Filters[i] != want[i] {
			t.Fatalf("filter[%d] = %#v, want %#v", i, query.Filters[i], want[i])
		}
	}
}

func TestProfileAggregationQueryKeepsHostOnlySemantics(t *testing.T) {
	query := buildProfileAggregationQuery(&SearchFilter{Hostname: "node-a"})
	want := []driver.Filter{
		{Field: profileFieldHostname + ".keyword", Op: driver.OpEq, Value: "node-a"},
		{Field: profileFieldContainerHostname, Op: driver.OpEq, Value: ""},
	}
	if len(query.Filters) != len(want) {
		t.Fatalf("filters = %#v, want %#v", query.Filters, want)
	}
	for i := range want {
		if query.Filters[i] != want[i] {
			t.Fatalf("filter[%d] = %#v, want %#v", i, query.Filters[i], want[i])
		}
	}
}

func TestProfileAggregationQueryCombinesHostAndContainer(t *testing.T) {
	query := buildProfileAggregationQuery(&SearchFilter{
		Hostname:          "node-a",
		ContainerHostname: "worker",
	})
	want := []driver.Filter{
		{Field: profileFieldHostname + ".keyword", Op: driver.OpEq, Value: "node-a"},
		{Field: profileFieldContainerHostname + ".keyword", Op: driver.OpEq, Value: "worker"},
	}
	if len(query.Filters) != len(want) {
		t.Fatalf("filters = %#v, want %#v", query.Filters, want)
	}
	for i := range want {
		if query.Filters[i] != want[i] {
			t.Fatalf("filter[%d] = %#v, want %#v", i, query.Filters[i], want[i])
		}
	}
}

func TestProfileAggregationQuerySupportsContainerWithoutHost(t *testing.T) {
	query := buildProfileAggregationQuery(&SearchFilter{ContainerHostname: "worker"})
	want := driver.Filter{
		Field: profileFieldContainerHostname + ".keyword",
		Op:    driver.OpEq,
		Value: "worker",
	}
	if len(query.Filters) != 1 || query.Filters[0] != want {
		t.Fatalf("filters = %#v, want [%#v]", query.Filters, want)
	}
}

func TestProfileAggregationQueryCanIncludeContainerCandidatesForHost(t *testing.T) {
	query := buildProfileAggregationQuery(&SearchFilter{
		Hostname:                 "node-a",
		IncludeContainerProfiles: true,
	})
	want := driver.Filter{
		Field: profileFieldHostname + ".keyword",
		Op:    driver.OpEq,
		Value: "node-a",
	}
	if len(query.Filters) != 1 || query.Filters[0] != want {
		t.Fatalf("filters = %#v, want [%#v]", query.Filters, want)
	}
}

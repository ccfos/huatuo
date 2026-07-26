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
	"reflect"
	"testing"

	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/storage/driver"
)

func TestBuildProfileAggregationQueryPreservesTargetMatchers(t *testing.T) {
	query := buildProfileAggregationQuery(&SearchFilter{
		ContainerID:       "containerd://4df60fc5",
		ContainerHostname: "checkout-api-7b9f6d8c4f-k2x7m",
	})
	want := []driver.Filter{
		{
			Field: profileFieldContainerID + ".keyword",
			Op:    driver.OpEq,
			Value: "containerd://4df60fc5",
		},
		{
			Field: profileFieldContainerHostname + ".keyword",
			Op:    driver.OpEq,
			Value: "checkout-api-7b9f6d8c4f-k2x7m",
		},
	}

	if !reflect.DeepEqual(query.Filters, want) {
		t.Fatalf("query filters = %#v, want %#v", query.Filters, want)
	}
}

func TestBuildProfileAggregationQueryAddsTracerIDOnce(t *testing.T) {
	query := buildProfileAggregationQuery(&SearchFilter{TracerID: "task-20260722-8f6a"})
	want := []driver.Filter{
		{
			Field: profileFieldTracerID + ".keyword",
			Op:    driver.OpEq,
			Value: "task-20260722-8f6a",
		},
	}

	if !reflect.DeepEqual(query.Filters, want) {
		t.Fatalf("query filters = %#v, want %#v", query.Filters, want)
	}
}

func TestProfileDocumentMapperIndexesCollectionDimensions(t *testing.T) {
	document := &ProfileDocument{}
	document.ContainerID = "containerd://abc"
	document.TracerData.Flamedata.Labels = map[string]string{
		profiler.LabelProfilingScope: "thread_group",
		profiler.LabelTGID:           "4242",
		profiler.LabelContainerID:    document.ContainerID,
	}

	fields, err := (profileDocumentMapper{}).Fields(document)
	if err != nil {
		t.Fatalf("Fields() error = %v", err)
	}
	if got := fields[profileLabelField(profiler.LabelProfilingScope)]; got != "thread_group" {
		t.Fatalf("profiling_scope field = %v, want thread_group", got)
	}
	if got := fields[profileLabelField(profiler.LabelTGID)]; got != "4242" {
		t.Fatalf("tgid field = %v, want 4242", got)
	}
	if _, duplicated := fields[profileLabelField(profiler.LabelContainerID)]; duplicated {
		t.Fatal("container_id duplicated under profile labels")
	}
}

func TestBuildProfileAggregationQueryAddsDimensionMatchers(t *testing.T) {
	query := buildProfileAggregationQuery(&SearchFilter{
		ContainerID: "containerd://abc",
		Labels: map[string]string{
			profiler.LabelProfilingScope: "thread_group",
			profiler.LabelTGID:           "4242",
			"unmanaged":                  "ignored",
		},
	})
	want := []driver.Filter{
		{
			Field: profileFieldContainerID + ".keyword",
			Op:    driver.OpEq,
			Value: "containerd://abc",
		},
		{
			Field: profileLabelKeywordField(profiler.LabelProfilingScope),
			Op:    driver.OpEq,
			Value: "thread_group",
		},
		{
			Field: profileLabelKeywordField(profiler.LabelTGID),
			Op:    driver.OpEq,
			Value: "4242",
		},
	}

	if !reflect.DeepEqual(query.Filters, want) {
		t.Fatalf("query filters = %#v, want %#v", query.Filters, want)
	}
}

func TestNormalizeProfileAggregationFieldSupportsDimensions(t *testing.T) {
	tests := map[string]string{
		profiler.LabelPID:         profileLabelKeywordField(profiler.LabelPID),
		profiler.LabelTGID:        profileLabelKeywordField(profiler.LabelTGID),
		profiler.LabelContainerID: profileFieldContainerID + ".keyword",
	}
	for input, want := range tests {
		got, err := normalizeProfileAggregationField(input)
		if err != nil {
			t.Fatalf("normalizeProfileAggregationField(%q) error = %v", input, err)
		}
		if got != want {
			t.Errorf("normalizeProfileAggregationField(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestBuildProfileSearchQueryUsesStablePaginationOrder(t *testing.T) {
	query := buildProfileSearchQuery(&SearchFilter{Limit: 1000, Offset: 1000})
	want := []driver.Sort{
		{Field: profileFieldUploadedTime, Desc: true},
		{Field: profileFieldTracerID},
	}

	if !reflect.DeepEqual(query.Sorts, want) {
		t.Fatalf("query sorts = %#v, want %#v", query.Sorts, want)
	}
}

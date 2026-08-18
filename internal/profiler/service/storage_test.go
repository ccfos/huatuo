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
	"time"

	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/storage/driver"
)

func TestBuildProfileAggregationQueryUsesHalfOpenTimeRange(t *testing.T) {
	start := time.Date(2026, time.August, 18, 9, 0, 0, 123456789, time.UTC)
	end := time.Date(2026, time.August, 18, 9, 0, 1, 987654321, time.UTC)
	query := buildProfileAggregationQuery(&SearchFilter{
		StartTime: start,
		EndTime:   end,
	})
	want := []driver.Filter{
		{
			Field: profileFieldUploadedTime,
			Op:    driver.OpGte,
			Value: start.Format(time.RFC3339Nano),
		},
		{
			Field: profileFieldUploadedTime,
			Op:    driver.OpLt,
			Value: end.Format(time.RFC3339Nano),
		},
	}

	if !reflect.DeepEqual(query.Filters, want) {
		t.Fatalf("query filters = %#v, want %#v", query.Filters, want)
	}
}

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

func TestProfileDocumentMapperUsesProfileStorageID(t *testing.T) {
	mapper := profileDocumentMapper{}
	document := &ProfileDocument{
		ProfileStorageID: "window-1",
		TracerID:         "trace-1",
	}
	if got := mapper.ID(document); got != "window-1" {
		t.Fatalf("ID() = %q, want window-1", got)
	}

	document.ProfileStorageID = ""
	if got := mapper.ID(document); got != "trace-1" {
		t.Fatalf("legacy ID() = %q, want trace-1", got)
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

func TestNormalizeProfileAggregationFieldSupportsDimensionsAndAliases(t *testing.T) {
	tests := map[string]string{
		profiler.LabelProfileID:   profileFieldTracerID + ".keyword",
		profiler.LabelTracer:      profileFieldTracerID + ".keyword",
		profiler.LabelPID:         profileLabelKeywordField(profiler.LabelPID),
		profiler.LabelTGID:        profileLabelKeywordField(profiler.LabelTGID),
		profiler.LabelContainerID: profileFieldContainerID + ".keyword",
		profileFieldProfileType:   profileFieldProfileType + ".keyword",
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
		{Field: profileFieldTracerID + ".keyword"},
		{Field: profileFieldStorageID + ".keyword"},
	}

	if !reflect.DeepEqual(query.Sorts, want) {
		t.Fatalf("query sorts = %#v, want %#v", query.Sorts, want)
	}
}

func TestBuildProfileAggregationQueryFiltersByRegion(t *testing.T) {
	query := buildProfileAggregationQuery(&SearchFilter{
		Region:   "cn-beijing",
		Hostname: "node-1",
	})

	found := false
	for _, f := range query.Filters {
		if f.Field == profileFieldRegion+".keyword" && f.Value == "cn-beijing" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("query filters = %#v, want region filter present", query.Filters)
	}
}

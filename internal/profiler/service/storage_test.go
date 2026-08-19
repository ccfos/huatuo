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

func TestNormalizeProfileAggregationFieldUsesKeywords(t *testing.T) {
	tests := map[string]string{
		"id":                    profileFieldTracerID + ".keyword",
		"tracer":                profileFieldTracerID + ".keyword",
		profileFieldContainerID: profileFieldContainerID + ".keyword",
		profileFieldProfileType: profileFieldProfileType + ".keyword",
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

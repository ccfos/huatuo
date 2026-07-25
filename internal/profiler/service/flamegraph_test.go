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
	"strings"
	"testing"

	"huatuo-bamai/internal/profiler"

	"github.com/prometheus/prometheus/model/labels"
)

func TestServiceReadyRejectsUninitializedStorage(t *testing.T) {
	err := (*Service)(nil).Ready(t.Context())
	if err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("Ready() error = %v, want initialization error", err)
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

func TestApplyProfileMatcherAcceptsCollectionDimensions(t *testing.T) {
	filter := &SearchFilter{}
	matcher := labels.MustNewMatcher(labels.MatchEqual, profiler.LabelTGID, "4242")

	if err := applyProfileMatcher(filter, matcher); err != nil {
		t.Fatalf("applyProfileMatcher() error = %v", err)
	}
	if got := filter.Labels[profiler.LabelTGID]; got != "4242" {
		t.Fatalf("tgid matcher = %q, want 4242", got)
	}
}

func TestLabelNamesIncludesCollectionDimensions(t *testing.T) {
	response, err := (&Service{}).LabelNames(t.Context(), nil)
	if err != nil {
		t.Fatalf("LabelNames() error = %v", err)
	}
	names := make(map[string]struct{}, len(response.Names))
	for _, name := range response.Names {
		names[name] = struct{}{}
	}
	for _, name := range profiler.CollectionDimensionLabelNames() {
		if _, ok := names[name]; !ok {
			t.Errorf("LabelNames() missing %q", name)
		}
	}
}

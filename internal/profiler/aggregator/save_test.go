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

package aggregator

import (
	"testing"
	"time"

	"huatuo-bamai/internal/profiler"
	profctx "huatuo-bamai/internal/profiler/context"
)

func TestApplyCollectionDimensionLabelsUsesProfilerContext(t *testing.T) {
	data, err := profiler.ParseTree(
		time.Unix(1, 0),
		profiler.ProfileTypeCpuSample,
		[]*profiler.TreeItem{{Stack: [][]byte{[]byte("work")}, Value: 1}},
		&profiler.ParseOption{SampleRate: 99},
	)
	if err != nil {
		t.Fatalf("ParseTree() error = %v", err)
	}

	got, err := applyCollectionDimensionLabels(&profctx.ProfilerContext{
		PIDs:        []int{42},
		ThreadGroup: true,
	}, data)
	if err != nil {
		t.Fatalf("applyCollectionDimensionLabels() error = %v", err)
	}
	if got.Labels[profiler.LabelProfilingScope] != "thread_group" {
		t.Fatalf(
			"profiling_scope = %q, want thread_group",
			got.Labels[profiler.LabelProfilingScope],
		)
	}
	if got.Labels[profiler.LabelTGID] != "42" {
		t.Fatalf("tgid = %q, want 42", got.Labels[profiler.LabelTGID])
	}
	if len(got.Profile.Sample) != 1 ||
		len(got.Profile.Sample[0].Label) != len(got.Labels) {
		t.Fatalf("sample labels = %#v, profile labels = %#v", got.Profile.Sample, got.Labels)
	}
}

func TestApplyCollectionDimensionLabelsRejectsNonProfileData(t *testing.T) {
	_, err := applyCollectionDimensionLabels(&profctx.ProfilerContext{}, "not a profile")
	if err == nil {
		t.Fatal("applyCollectionDimensionLabels() error = nil")
	}
}

func TestApplyCollectionDimensionLabelsAcceptsNilContext(t *testing.T) {
	data, err := profiler.ParseTree(
		time.Unix(1, 0),
		profiler.ProfileTypeCpuSample,
		[]*profiler.TreeItem{{Stack: [][]byte{[]byte("work")}, Value: 1}},
		&profiler.ParseOption{SampleRate: 99},
	)
	if err != nil {
		t.Fatalf("ParseTree() error = %v", err)
	}

	got, err := applyCollectionDimensionLabels(nil, data)
	if err != nil {
		t.Fatalf("applyCollectionDimensionLabels() error = %v", err)
	}
	if got != data {
		t.Fatalf("profile pointer = %p, want %p", got, data)
	}
	if got.Labels != nil || len(got.Profile.Sample[0].Label) != 0 {
		t.Fatalf("nil context added labels: %#v", got)
	}
}

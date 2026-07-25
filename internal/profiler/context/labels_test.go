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

package context

import (
	"reflect"
	"testing"

	"huatuo-bamai/internal/profiler"
)

func TestCollectionDimensionLabels(t *testing.T) {
	tests := []struct {
		name string
		pctx *ProfilerContext
		want map[string]string
	}{
		{
			name: "host",
			pctx: &ProfilerContext{},
			want: map[string]string{profiler.LabelProfilingScope: profilingScopeHost},
		},
		{
			name: "exact pid and CPUs",
			pctx: &ProfilerContext{PIDs: []int{42}, CPUIDs: []int{3, 1}},
			want: map[string]string{
				profiler.LabelProfilingScope: profilingScopePID,
				profiler.LabelPID:            "42",
				profiler.LabelCPU:            "1,3",
			},
		},
		{
			name: "thread group",
			pctx: &ProfilerContext{PIDs: []int{42}, ThreadGroup: true},
			want: map[string]string{
				profiler.LabelProfilingScope: profilingScopeThreadGroup,
				profiler.LabelTGID:           "42",
			},
		},
		{
			name: "container",
			pctx: &ProfilerContext{ContainerID: "containerd://abc"},
			want: map[string]string{
				profiler.LabelProfilingScope: profilingScopeContainer,
				profiler.LabelContainerID:    "containerd://abc",
			},
		},
		{
			name: "multiple external profiler targets",
			pctx: &ProfilerContext{PIDs: []int{42, 99}},
			want: map[string]string{
				profiler.LabelProfilingScope: profilingScopePID,
				profiler.LabelPID:            "42,99",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.pctx.CollectionDimensionLabels(); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("CollectionDimensionLabels() = %#v, want %#v", got, test.want)
			}
		})
	}
}

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

package profiler

import (
	"reflect"
	"testing"
	"time"
)

func TestApplyLabelsInjectsAndReplacesPprofLabels(t *testing.T) {
	data, err := ParseTree(time.Unix(1, 0), ProfileTypeCpuSample, []*TreeItem{
		{
			Stack: [][]byte{[]byte("process"), []byte("work")},
			Value: 1,
		},
		{
			Stack: [][]byte{[]byte("process"), []byte("wait")},
			Value: 2,
		},
	}, &ParseOption{SampleRate: 99})
	if err != nil {
		t.Fatalf("ParseTree() error = %v", err)
	}

	if err := ApplyLabels(data, map[string]string{
		LabelProfilingScope: "thread_group",
		LabelTGID:           "4242",
	}); err != nil {
		t.Fatalf("ApplyLabels() error = %v", err)
	}
	if err := ApplyLabels(data, map[string]string{LabelTGID: "5252"}); err != nil {
		t.Fatalf("ApplyLabels() replacement error = %v", err)
	}

	if got, want := data.Labels, map[string]string{
		LabelProfilingScope: "thread_group",
		LabelTGID:           "5252",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("profile labels = %#v, want %#v", got, want)
	}
	if len(data.Profile.Sample) != 2 {
		t.Fatalf("samples = %d, want 2", len(data.Profile.Sample))
	}

	for _, sample := range data.Profile.Sample {
		resolved := make(map[string]string)
		for _, label := range sample.Label {
			resolved[data.Profile.StringTable[label.Key]] = data.Profile.StringTable[label.Str]
		}
		if !reflect.DeepEqual(resolved, data.Labels) {
			t.Fatalf("pprof labels = %#v, want %#v", resolved, data.Labels)
		}
		if got := len(sample.Label); got != len(resolved) {
			t.Fatalf("pprof label entries = %d, unique labels = %d", got, len(resolved))
		}
	}
}

func TestApplyLabelsValidatesBeforeMutation(t *testing.T) {
	data := &ProfileData{}
	err := ApplyLabels(data, map[string]string{
		LabelPID:   "42",
		"bad-name": "value",
	})
	if err == nil {
		t.Fatal("ApplyLabels() error = nil, want invalid label error")
	}
	if data.Labels != nil || len(data.Profile.StringTable) != 0 {
		t.Fatalf("profile mutated after validation error: %#v", data)
	}
}

func TestApplyLabelsRejectsMalformedStringTable(t *testing.T) {
	data := &ProfileData{}
	data.Profile.StringTable = []string{"not-empty"}

	err := ApplyLabels(data, map[string]string{LabelPID: "42"})
	if err == nil {
		t.Fatal("ApplyLabels() error = nil, want malformed string table error")
	}
	if data.Labels != nil || len(data.Profile.StringTable) != 1 {
		t.Fatalf("profile mutated after string table error: %#v", data)
	}
}

func TestCollectionDimensionLabelNamesReturnsCopy(t *testing.T) {
	labels := CollectionDimensionLabelNames()
	labels[0] = "changed"

	if got := CollectionDimensionLabelNames()[0]; got != LabelProfilingScope {
		t.Fatalf("first collection label = %q, want %q", got, LabelProfilingScope)
	}
}

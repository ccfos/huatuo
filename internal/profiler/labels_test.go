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
	"testing"
	"time"
)

func TestApplyLabelsEmbedsProfileAndPprofLabels(t *testing.T) {
	data, err := ParseTree(time.Unix(1, 0), ProfileTypeCpuSample, []*TreeItem{{
		Stack: [][]byte{[]byte("process"), []byte("work")},
		Value: 1,
	}}, &ParseOption{SampleRate: 99})
	if err != nil {
		t.Fatalf("ParseTree() error = %v", err)
	}

	labels := map[string]string{
		LabelProfilingScope: "tgid",
		LabelTGID:           "4242",
	}
	if err := ApplyLabels(data, labels); err != nil {
		t.Fatalf("ApplyLabels() error = %v", err)
	}
	if data.Labels[LabelTGID] != "4242" {
		t.Fatalf("profile labels = %v", data.Labels)
	}
	if len(data.Profile.Sample) == 0 || len(data.Profile.Sample[0].Label) != 2 {
		t.Fatalf("pprof sample labels = %#v", data.Profile.Sample)
	}

	resolved := make(map[string]string)
	for _, label := range data.Profile.Sample[0].Label {
		resolved[data.Profile.StringTable[label.Key]] = data.Profile.StringTable[label.Str]
	}
	if resolved[LabelProfilingScope] != "tgid" || resolved[LabelTGID] != "4242" {
		t.Fatalf("resolved labels = %v", resolved)
	}

	if err := ApplyLabels(data, map[string]string{LabelTGID: "5252"}); err != nil {
		t.Fatalf("ApplyLabels() replacement error = %v", err)
	}
	count := 0
	for _, label := range data.Profile.Sample[0].Label {
		if data.Profile.StringTable[label.Key] == LabelTGID {
			count++
			if got := data.Profile.StringTable[label.Str]; got != "5252" {
				t.Fatalf("tgid label = %q, want 5252", got)
			}
		}
	}
	if count != 1 {
		t.Fatalf("tgid label count = %d, want 1", count)
	}
}

func TestApplyLabelsRejectsInvalidName(t *testing.T) {
	data := &ProfileData{}
	if err := ApplyLabels(data, map[string]string{"bad-label": "value"}); err == nil {
		t.Fatal("ApplyLabels() error = nil, want invalid label error")
	}
}

func TestValidateCustomLabelNameRejectsCollectionDimensions(t *testing.T) {
	if err := ValidateCustomLabelName("service"); err != nil {
		t.Fatalf("ValidateCustomLabelName(service) error = %v", err)
	}
	for _, name := range CollectionDimensionLabels {
		if err := ValidateCustomLabelName(name); err == nil {
			t.Errorf("ValidateCustomLabelName(%q) error = nil, want reserved label error", name)
		}
	}
}

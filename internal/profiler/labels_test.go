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

	ptree "github.com/grafana/pyroscope/pkg/og/storage/tree"
)

func TestApplyCollectionDimensionLabelsInjectsAndReplaces(t *testing.T) {
	data := newLabelTestProfile(t)
	data.Labels = map[string]string{"legacy": "preserved"}
	customKey := appendProfileString(&data.Profile, "legacy")
	customValue := appendProfileString(&data.Profile, "value")
	for _, sample := range data.Profile.Sample {
		sample.Label = append(sample.Label, &ptree.Label{
			Key: customKey,
			Str: customValue,
		})
	}

	first := map[string]string{
		LabelProfilingScope: "thread_group",
		LabelTGID:           "4242",
	}
	if err := ApplyCollectionDimensionLabels(data, first); err != nil {
		t.Fatalf("ApplyCollectionDimensionLabels() error = %v", err)
	}
	if err := ApplyCollectionDimensionLabels(data, first); err != nil {
		t.Fatalf("idempotent ApplyCollectionDimensionLabels() error = %v", err)
	}
	if err := ApplyCollectionDimensionLabels(
		data,
		map[string]string{
			LabelProfilingScope: "thread_group",
			LabelTGID:           "5252",
		},
	); err != nil {
		t.Fatalf("replacement ApplyCollectionDimensionLabels() error = %v", err)
	}

	wantMetadata := map[string]string{
		"legacy":            "preserved",
		LabelProfilingScope: "thread_group",
		LabelTGID:           "5252",
	}
	if !reflect.DeepEqual(data.Labels, wantMetadata) {
		t.Fatalf("profile labels = %#v, want %#v", data.Labels, wantMetadata)
	}

	for _, sample := range data.Profile.Sample {
		got := resolveStringLabels(t, data.Profile.StringTable, sample.Label)
		want := map[string]string{
			"legacy":            "value",
			LabelProfilingScope: "thread_group",
			LabelTGID:           "5252",
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sample labels = %#v, want %#v", got, want)
		}
		if len(sample.Label) != len(got) {
			t.Fatalf("sample contains duplicate labels: %#v", sample.Label)
		}
	}
}

func TestApplyCollectionDimensionLabelsClearsStaleManagedLabels(t *testing.T) {
	data := newLabelTestProfile(t)

	if err := ApplyCollectionDimensionLabels(data, map[string]string{
		LabelProfilingScope: "container",
		LabelContainerID:    "containerd://abc",
		LabelCPU:            "1,3",
	}); err != nil {
		t.Fatalf("container ApplyCollectionDimensionLabels() error = %v", err)
	}
	if err := ApplyCollectionDimensionLabels(data, map[string]string{
		LabelProfilingScope: "host",
	}); err != nil {
		t.Fatalf("host ApplyCollectionDimensionLabels() error = %v", err)
	}

	want := map[string]string{LabelProfilingScope: "host"}
	if !reflect.DeepEqual(data.Labels, want) {
		t.Fatalf("profile labels = %#v, want %#v", data.Labels, want)
	}
	for _, sample := range data.Profile.Sample {
		got := resolveStringLabels(t, data.Profile.StringTable, sample.Label)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("sample labels = %#v, want %#v", got, want)
		}
	}
}

func TestApplyCollectionDimensionLabelsRejectsUnmanagedLabelBeforeMutation(t *testing.T) {
	data := newLabelTestProfile(t)
	beforeStrings := append([]string(nil), data.Profile.StringTable...)

	err := ApplyCollectionDimensionLabels(data, map[string]string{
		LabelPID: "42",
		"custom": "not-allowed",
	})
	if err == nil {
		t.Fatal("ApplyCollectionDimensionLabels() error = nil")
	}
	if data.Labels != nil || !reflect.DeepEqual(data.Profile.StringTable, beforeStrings) {
		t.Fatalf("profile mutated after validation error: %#v", data)
	}
}

func TestApplyCollectionDimensionLabelsRejectsMalformedProfileBeforeMutation(t *testing.T) {
	tests := []struct {
		name string
		data *ProfileData
	}{
		{
			name: "non-empty first string",
			data: &ProfileData{Profile: ptree.Profile{
				StringTable: []string{"invalid"},
			}},
		},
		{
			name: "label key outside string table",
			data: &ProfileData{Profile: ptree.Profile{
				StringTable: []string{""},
				Sample: []*ptree.Sample{{
					Label: []*ptree.Label{{Key: 2, Str: 0}},
				}},
			}},
		},
		{
			name: "label value outside string table",
			data: &ProfileData{Profile: ptree.Profile{
				StringTable: []string{""},
				Sample: []*ptree.Sample{{
					Label: []*ptree.Label{{Key: 0, Str: 2}},
				}},
			}},
		},
		{
			name: "label unit outside string table",
			data: &ProfileData{Profile: ptree.Profile{
				StringTable: []string{""},
				Sample: []*ptree.Sample{{
					Label: []*ptree.Label{{Key: 0, Str: 0, NumUnit: 2}},
				}},
			}},
		},
		{
			name: "label without string table",
			data: &ProfileData{Profile: ptree.Profile{
				Sample: []*ptree.Sample{{
					Label: []*ptree.Label{{Key: 0, Str: 0}},
				}},
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			beforeStrings := append([]string(nil), test.data.Profile.StringTable...)

			err := ApplyCollectionDimensionLabels(
				test.data,
				map[string]string{LabelPID: "42"},
			)
			if err == nil {
				t.Fatal("ApplyCollectionDimensionLabels() error = nil")
			}
			if test.data.Labels != nil ||
				!reflect.DeepEqual(test.data.Profile.StringTable, beforeStrings) {
				t.Fatalf("profile mutated after validation error: %#v", test.data)
			}
		})
	}
}

func TestApplyCollectionDimensionLabelsKeepsLegacyProfileUnchanged(t *testing.T) {
	data := newLabelTestProfile(t)
	beforeStrings := append([]string(nil), data.Profile.StringTable...)

	if err := ApplyCollectionDimensionLabels(data, nil); err != nil {
		t.Fatalf("ApplyCollectionDimensionLabels() error = %v", err)
	}
	if data.Labels != nil || !reflect.DeepEqual(data.Profile.StringTable, beforeStrings) {
		t.Fatalf("legacy profile mutated: %#v", data)
	}
}

func TestCollectionDimensionLabelNamesReturnsCopy(t *testing.T) {
	names := CollectionDimensionLabelNames()
	names[0] = "changed"

	if got := CollectionDimensionLabelNames()[0]; got != LabelProfilingScope {
		t.Fatalf("first collection label = %q, want %q", got, LabelProfilingScope)
	}
}

func newLabelTestProfile(t *testing.T) *ProfileData {
	t.Helper()

	data, err := ParseTree(
		time.Unix(1, 0),
		ProfileTypeCpuSample,
		[]*TreeItem{
			{Stack: [][]byte{[]byte("process"), []byte("work")}, Value: 1},
			{Stack: [][]byte{[]byte("process"), []byte("wait")}, Value: 2},
		},
		&ParseOption{SampleRate: 99},
	)
	if err != nil {
		t.Fatalf("ParseTree() error = %v", err)
	}
	return data
}

func appendProfileString(profile *ptree.Profile, value string) int64 {
	index := int64(len(profile.StringTable))
	profile.StringTable = append(profile.StringTable, value)
	return index
}

func resolveStringLabels(
	t *testing.T,
	stringTable []string,
	labels []*ptree.Label,
) map[string]string {
	t.Helper()

	result := make(map[string]string, len(labels))
	for _, label := range labels {
		if label.Key < 0 || label.Key >= int64(len(stringTable)) ||
			label.Str < 0 || label.Str >= int64(len(stringTable)) {
			t.Fatalf("invalid label indexes: %#v", label)
		}
		result[stringTable[label.Key]] = stringTable[label.Str]
	}
	return result
}

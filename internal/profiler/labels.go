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
	"fmt"
	"regexp"
	"sort"

	ptree "github.com/grafana/pyroscope/pkg/og/storage/tree"
)

const (
	// LabelProfilingScope identifies the target selector used for collection.
	LabelProfilingScope = "profiling_scope"
	// LabelCPU identifies the CPUs selected for native CPU collection.
	LabelCPU = "cpu"
	// LabelPID identifies exact process or thread targets.
	LabelPID = "pid"
	// LabelTGID identifies a target thread group.
	LabelTGID = "tgid"
	// LabelContainerID identifies a target through the common container selector.
	LabelContainerID = "container_id"
)

var (
	collectionDimensionLabels = [...]string{
		LabelProfilingScope,
		LabelCPU,
		LabelPID,
		LabelTGID,
		LabelContainerID,
	}
	labelNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// CollectionDimensionLabelNames returns labels managed by collection filters.
func CollectionDimensionLabelNames() []string {
	labels := make([]string, len(collectionDimensionLabels))
	copy(labels, collectionDimensionLabels[:])
	return labels
}

// IsCollectionDimensionLabel reports whether name is managed by the profiler.
func IsCollectionDimensionLabel(name string) bool {
	switch name {
	case LabelProfilingScope, LabelCPU, LabelPID, LabelTGID, LabelContainerID:
		return true
	default:
		return false
	}
}

// ApplyLabels injects profile-wide string labels into every pprof sample.
func ApplyLabels(data *ProfileData, labels map[string]string) error {
	if data == nil || len(labels) == 0 {
		return nil
	}

	keys := make([]string, 0, len(labels))
	for key, value := range labels {
		if value == "" {
			continue
		}
		if !labelNamePattern.MatchString(key) {
			return fmt.Errorf("invalid profiling label name %q", key)
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)

	if len(data.Profile.StringTable) == 0 {
		data.Profile.StringTable = append(data.Profile.StringTable, "")
	} else if data.Profile.StringTable[0] != "" {
		return fmt.Errorf("invalid pprof string table: first entry must be empty")
	}

	if data.Labels == nil {
		data.Labels = make(map[string]string, len(keys))
	}
	for _, key := range keys {
		data.Labels[key] = labels[key]
	}

	stringIndexes := make(map[string]int64, len(data.Profile.StringTable)+len(keys)*2)
	for i, value := range data.Profile.StringTable {
		stringIndexes[value] = int64(i)
	}
	stringIndex := func(value string) int64 {
		if index, ok := stringIndexes[value]; ok {
			return index
		}
		index := int64(len(data.Profile.StringTable))
		data.Profile.StringTable = append(data.Profile.StringTable, value)
		stringIndexes[value] = index
		return index
	}

	keyNames := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		keyNames[key] = struct{}{}
		stringIndex(key)
	}
	keyIndexes := make(map[int64]struct{}, len(keys))
	for index, value := range data.Profile.StringTable {
		if _, replaced := keyNames[value]; replaced {
			keyIndexes[int64(index)] = struct{}{}
		}
	}

	for _, sample := range data.Profile.Sample {
		if sample == nil {
			continue
		}

		kept := sample.Label[:0]
		for _, label := range sample.Label {
			if label == nil {
				continue
			}
			if _, replaced := keyIndexes[label.Key]; !replaced {
				kept = append(kept, label)
			}
		}
		sample.Label = kept
		for _, key := range keys {
			sample.Label = append(sample.Label, &ptree.Label{
				Key: stringIndex(key),
				Str: stringIndex(labels[key]),
			})
		}
		sort.Slice(sample.Label, func(i, j int) bool {
			if sample.Label[i].Key != sample.Label[j].Key {
				return sample.Label[i].Key < sample.Label[j].Key
			}
			return sample.Label[i].Str < sample.Label[j].Str
		})
	}

	return nil
}

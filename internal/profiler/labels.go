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

var collectionDimensionLabels = [...]string{
	LabelProfilingScope,
	LabelCPU,
	LabelPID,
	LabelTGID,
	LabelContainerID,
}

// CollectionDimensionLabelNames returns the labels managed by collection.
func CollectionDimensionLabelNames() []string {
	names := make([]string, len(collectionDimensionLabels))
	copy(names, collectionDimensionLabels[:])
	return names
}

// IsCollectionDimensionLabel reports whether name is managed by collection.
func IsCollectionDimensionLabel(name string) bool {
	switch name {
	case LabelProfilingScope, LabelCPU, LabelPID, LabelTGID, LabelContainerID:
		return true
	default:
		return false
	}
}

// ApplyCollectionDimensionLabels mirrors managed dimensions into every sample.
func ApplyCollectionDimensionLabels(data *ProfileData, labels map[string]string) error {
	if data == nil || len(labels) == 0 {
		return nil
	}

	keys := make([]string, 0, len(labels))
	for key, value := range labels {
		if !IsCollectionDimensionLabel(key) {
			return fmt.Errorf("unsupported collection dimension label %q", key)
		}
		if value != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)

	if len(data.Profile.StringTable) == 0 {
		for _, sample := range data.Profile.Sample {
			if sample != nil && len(sample.Label) > 0 {
				return fmt.Errorf("invalid pprof label: string table is empty")
			}
		}
	} else if data.Profile.StringTable[0] != "" {
		return fmt.Errorf("invalid pprof string table: first entry must be empty")
	}
	for _, sample := range data.Profile.Sample {
		if sample == nil {
			continue
		}
		for _, label := range sample.Label {
			if label == nil {
				continue
			}
			if label.Key < 0 || label.Key >= int64(len(data.Profile.StringTable)) {
				return fmt.Errorf("invalid pprof label key index %d", label.Key)
			}
			if label.Str < 0 || label.Str >= int64(len(data.Profile.StringTable)) {
				return fmt.Errorf("invalid pprof label string index %d", label.Str)
			}
			if label.NumUnit < 0 || label.NumUnit >= int64(len(data.Profile.StringTable)) {
				return fmt.Errorf("invalid pprof label unit index %d", label.NumUnit)
			}
		}
	}
	if len(data.Profile.StringTable) == 0 && len(keys) > 0 {
		data.Profile.StringTable = append(data.Profile.StringTable, "")
	}

	if data.Labels == nil && len(keys) > 0 {
		data.Labels = make(map[string]string, len(keys))
	}
	for _, key := range collectionDimensionLabels {
		delete(data.Labels, key)
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

	for _, key := range keys {
		stringIndex(key)
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
			if !IsCollectionDimensionLabel(data.Profile.StringTable[label.Key]) {
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
	}

	return nil
}

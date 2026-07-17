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
	LabelProfilingScope = "profiling_scope"
	LabelPID            = "pid"
	LabelTGID           = "tgid"
	LabelCgroupID       = "cgroup_id"
	LabelCgroupPath     = "cgroup_path"
	LabelProcessGroupID = "process_group_id"
	LabelContainerID    = "container_id"
	LabelLockType       = "lock_type"
)

// CollectionDimensionLabels is the stable label set exposed by the
// Pyroscope-compatible query API.
var CollectionDimensionLabels = []string{
	LabelProfilingScope,
	LabelPID,
	LabelTGID,
	LabelCgroupID,
	LabelCgroupPath,
	LabelProcessGroupID,
	LabelContainerID,
	LabelLockType,
}

var labelNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateLabelName enforces the Prometheus label-name grammar used by
// Pyroscope and Parca.
func ValidateLabelName(name string) error {
	if !labelNamePattern.MatchString(name) {
		return fmt.Errorf("invalid profiling label name %q", name)
	}
	return nil
}

// IsCollectionDimensionLabel reports whether name is managed by the profiler
// itself. User-provided values for these labels are rejected so a profile
// cannot claim dimensions that differ from the BPF collection filter.
func IsCollectionDimensionLabel(name string) bool {
	for _, label := range CollectionDimensionLabels {
		if name == label {
			return true
		}
	}
	return false
}

// ValidateCustomLabelName validates a user-supplied label and rejects names
// reserved for collection dimensions.
func ValidateCustomLabelName(name string) error {
	if err := ValidateLabelName(name); err != nil {
		return err
	}
	if IsCollectionDimensionLabel(name) {
		return fmt.Errorf("profiling label name %q is reserved", name)
	}
	return nil
}

// ApplyLabels merges profile-wide labels and injects them into every pprof
// sample. String-table indices are used as required by the pprof schema.
func ApplyLabels(data *ProfileData, labels map[string]string) error {
	if data == nil || len(labels) == 0 {
		return nil
	}

	if data.Labels == nil {
		data.Labels = make(map[string]string, len(labels))
	}

	keys := make([]string, 0, len(labels))
	for key, value := range labels {
		if value == "" {
			continue
		}
		if err := ValidateLabelName(key); err != nil {
			return err
		}
		data.Labels[key] = value
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)

	stringIndexes := make(map[string]int64, len(data.Profile.StringTable)+len(keys)*2)
	for i, value := range data.Profile.StringTable {
		stringIndexes[value] = int64(i)
	}
	stringIndex := func(value string) int64 {
		if idx, ok := stringIndexes[value]; ok {
			return idx
		}
		idx := int64(len(data.Profile.StringTable))
		data.Profile.StringTable = append(data.Profile.StringTable, value)
		stringIndexes[value] = idx
		return idx
	}

	for _, sample := range data.Profile.Sample {
		if sample == nil {
			continue
		}

		// Applying labels more than once replaces profile-wide values instead
		// of producing duplicate keys in a sample.
		replaced := make(map[int64]struct{}, len(keys))
		for _, key := range keys {
			replaced[stringIndex(key)] = struct{}{}
		}
		kept := sample.Label[:0]
		for _, label := range sample.Label {
			if _, ok := replaced[label.Key]; !ok {
				kept = append(kept, label)
			}
		}
		sample.Label = kept

		for _, key := range keys {
			sample.Label = append(sample.Label, &ptree.Label{
				Key: stringIndex(key),
				Str: stringIndex(data.Labels[key]),
			})
		}
	}

	return nil
}

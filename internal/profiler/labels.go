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

// CollectionDimensionLabelNames returns labels managed by collection filters.
func CollectionDimensionLabelNames() []string {
	names := make([]string, len(collectionDimensionLabels))
	copy(names, collectionDimensionLabels[:])
	return names
}

// IsCollectionDimensionLabel reports whether name is a supported display dimension.
func IsCollectionDimensionLabel(name string) bool {
	switch name {
	case LabelProfilingScope, LabelCPU, LabelPID, LabelTGID, LabelContainerID:
		return true
	default:
		return false
	}
}

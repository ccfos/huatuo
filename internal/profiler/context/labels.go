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
	"sort"
	"strconv"
	"strings"

	"huatuo-bamai/internal/profiler"
)

const (
	profilingScopeHost        = "host"
	profilingScopeContainer   = "container"
	profilingScopePID         = "pid"
	profilingScopeThreadGroup = "thread_group"
)

// CollectionDimensionLabels describes the selectors that produced a profile.
func (pctx *ProfilerContext) CollectionDimensionLabels() map[string]string {
	if pctx == nil {
		return nil
	}

	labels := map[string]string{
		profiler.LabelProfilingScope: profilingScopeHost,
	}
	if pctx.ContainerID != "" {
		labels[profiler.LabelProfilingScope] = profilingScopeContainer
		labels[profiler.LabelContainerID] = pctx.ContainerID
	} else if len(pctx.PIDs) > 0 {
		label := profiler.LabelPID
		labels[profiler.LabelProfilingScope] = profilingScopePID
		if pctx.ThreadGroup {
			label = profiler.LabelTGID
			labels[profiler.LabelProfilingScope] = profilingScopeThreadGroup
		}
		labels[label] = formatDimensionInts(pctx.PIDs)
	}
	if len(pctx.CPUIDs) > 0 {
		labels[profiler.LabelCPU] = formatDimensionInts(pctx.CPUIDs)
	}

	return labels
}

func formatDimensionInts(values []int) string {
	values = append([]int(nil), values...)
	sort.Ints(values)

	var result strings.Builder
	for i, value := range values {
		if i > 0 {
			result.WriteByte(',')
		}
		result.WriteString(strconv.Itoa(value))
	}
	return result.String()
}

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

package profiling

import (
	"sort"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/pkg/profiling"
)

func buildCapabilities(h *Handler) v1.ProfilingCapabilities {
	cpuLanguages := languageStrings(profiling.LanguagesFor(profiling.TypeCPU))
	sort.Strings(cpuLanguages)

	memoryLanguages := languageStrings(profiling.LanguagesFor(profiling.TypeMemory))
	sort.Strings(memoryLanguages)

	memoryModes := make(map[string][]string, len(memoryLanguages))
	for _, language := range profiling.LanguagesFor(profiling.TypeMemory) {
		modes := profiling.MemoryModesFor(language)
		values := make([]string, 0, len(modes))
		for _, mode := range modes {
			values = append(values, string(mode))
		}
		sort.Strings(values)
		memoryModes[string(language)] = values
	}

	cfg := h.profilingConfig

	return v1.ProfilingCapabilities{
		Types:                      []string{string(profiling.TypeCPU), string(profiling.TypeMemory)},
		CPULanguages:               cpuLanguages,
		MemoryLanguages:            memoryLanguages,
		MemoryModes:                memoryModes,
		AggregationIntervalSeconds: cfg.AggregationIntervalSeconds,
		MaxConcurrentProfilers:     cfg.MaxConcurrentProfilerProcesses,
	}
}

func languageStrings(languages []profiling.Language) []string {
	values := make([]string, 0, len(languages))
	for _, language := range languages {
		values = append(values, string(language))
	}
	return values
}

// capabilities returns the profiling capabilities supported by the server.
// This is a read-only endpoint that allows frontends, CLIs, and agents to
// discover supported profiling types, languages, memory modes, and default
// configuration values without hardcoding them.
func (h *Handler) capabilities(ctx *server.Context) error {
	response.Success(ctx, buildCapabilities(h))
	return nil
}

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
	"strings"

	v1 "huatuo-bamai/apis/v1"
	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
	"huatuo-bamai/pkg/profiling"
)

func buildCapabilitiesResponse(_ *Handler) (v1.ProfilingCapabilitiesResponse, error) {
	cpuLanguages := languageStrings(profiling.LanguagesFor(profiling.TypeCPU))
	sort.Strings(cpuLanguages)

	memoryLanguages := languageStrings(profiling.LanguagesFor(profiling.TypeMemory))
	sort.Strings(memoryLanguages)

	memoryModes := map[string]string{}
	for _, language := range profiling.LanguagesFor(profiling.TypeMemory) {
		implementation, _ := profiling.ImplementationFor(language)
		for _, mode := range profiling.MemoryModesFor(language) {
			id := strings.ToUpper(string(mode))
			if implementation == profiling.ImplementationNative {
				id = "NATIVE_" + id
			}
			memoryModes[id] = string(mode)
		}
	}

	cfg := config.Get().Profiling

	return v1.ProfilingCapabilitiesResponse{
		ProfileTypes:                    []string{string(profiling.TypeCPU), string(profiling.TypeMemory)},
		CPUSupportedLanguages:           cpuLanguages,
		MemorySupportedLanguages:        memoryLanguages,
		MemoryModes:                     memoryModes,
		DefaultCPUInterval:              cfg.CPUProfilingInterval,
		DefaultMemoryInterval:           cfg.MemoryProfilingInterval,
		DefaultCPUSingleTraceTimeout:    cfg.CPUSingleTraceTimeout,
		DefaultMemorySingleTraceTimeout: cfg.MemorySingleTraceTimeout,
		ThirdPartyToolLimit:             cfg.ThirdPartyToolLimit,
	}, nil
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
	resp, err := buildCapabilitiesResponse(h)
	if err != nil {
		return response.ErrInternal.WithMessage(err.Error())
	}
	response.Success(ctx, resp)
	return nil
}

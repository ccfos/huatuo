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
	"huatuo-bamai/cmd/huatuo-apiserver/config"
	"huatuo-bamai/internal/cgroups/subsystem"
	"huatuo-bamai/internal/server"
	"huatuo-bamai/internal/server/response"
)

// buildCapabilitiesResponse constructs the profiling capabilities response
// from the package-level supported languages/modes and the current configuration.
func buildCapabilitiesResponse(h *Handler) (v1.ProfilingCapabilitiesResponse, error) {
	cpuLanguages := make([]string, 0, len(supportedLanguages)+1)
	for lang := range supportedLanguages {
		cpuLanguages = append(cpuLanguages, lang)
	}
	// python is supported for CPU profiling via special handling in fillCPUTracerArgs
	cpuLanguages = append(cpuLanguages, "python")
	sort.Strings(cpuLanguages)

	memoryLanguages := make([]string, 0, len(supportedLanguages))
	for lang := range supportedLanguages {
		memoryLanguages = append(memoryLanguages, lang)
	}
	sort.Strings(memoryLanguages)

	// Copy memory modes to avoid mutating the package-level map
	memoryModes := make(map[string]string, len(supportedMemoryModes))
	for k, v := range supportedMemoryModes {
		memoryModes[k] = v
	}

	cfg := config.Get().Profiling

	return v1.ProfilingCapabilitiesResponse{
		ProfileTypes:                    []string{subsystem.SubsystemCPU, subsystem.SubsystemMemory, "lock"},
		CPUSupportedLanguages:           cpuLanguages,
		MemorySupportedLanguages:        memoryLanguages,
		MemoryModes:                     memoryModes,
		DefaultCPUInterval:              cfg.CPUProfilingInterval,
		DefaultMemoryInterval:           cfg.MemoryProfilingInterval,
		DefaultCPUSingleTraceTimeout:    cfg.CPUSingleTraceTimeout,
		DefaultMemorySingleTraceTimeout: cfg.MemorySingleTraceTimeout,
		ThirdPartyToolLimit:             cfg.ThirdPartyToolLimit,
		CollectionDimensions:            []string{"pid", "tgid", "cgroup", "process-group"},
		KernelLockTypes:                 []string{"mutex", "spinlock", "rwlock"},
	}, nil
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

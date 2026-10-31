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

package provider

import (
	"fmt"
	"slices"

	"github.com/ccfos/huatuo/internal/profiling/pipeline"
	profile "github.com/ccfos/huatuo/internal/profiling/profile"
	"github.com/ccfos/huatuo/pkg/profiling"
)

// New binds a private configuration copy to a fresh collection task.
func New(implementation profiling.Implementation, config *Config) (*profile.ProfilerMeta, error) {
	cfg := *config
	cfg.PIDs = slices.Clone(config.PIDs)
	cfg.CPUIDs = slices.Clone(config.CPUIDs)
	meta := &profile.ProfilerMeta{
		ToolName:       "profiler",
		Type:           cfg.Type,
		Implementation: implementation,
	}
	var err error
	meta.AggregationConfig.ContainerID = cfg.ContainerID
	meta.AggregationConfig.ProfileType, err = sampleType(&cfg)
	if err != nil {
		return nil, err
	}

	switch {
	case implementation == profiling.ImplementationNative && cfg.Type == profiling.TypeCPU:
		meta.Description = "Native CPU profiler using eBPF"
		meta.Impl = &cpuNativeProfiler{config: cfg}
	case implementation == profiling.ImplementationNative && cfg.Type == profiling.TypeMemory:
		if _, err := resolveMemMode(cfg.Mode); err != nil {
			return nil, err
		}

		meta.Description = "Native memory profiler using eBPF"
		meta.Impl = &memNativeProfiler{config: cfg}
	case implementation == profiling.ImplementationJava && cfg.Type == profiling.TypeCPU:
		meta.Description = "Java CPU profiler using async-profiler"
		meta.Impl = &javaProfiler{config: cfg}
	case implementation == profiling.ImplementationJava && cfg.Type == profiling.TypeMemory:
		meta.Description = "Java memory profiler using async-profiler"
		meta.Impl = &javaProfiler{config: cfg}
	case implementation == profiling.ImplementationPython && cfg.Type == profiling.TypeCPU:
		meta.Description = "Python CPU profiler using py-spy"
		meta.Impl = &pythonCPUProfiler{config: cfg}
		meta.AggregationMode = pipeline.AggregationModeOneShot
	default:
		return nil, fmt.Errorf("no profiler for implementation=%q type=%q", implementation, cfg.Type)
	}

	return meta, nil
}

func sampleType(cfg *Config) (string, error) {
	switch cfg.Type {
	case profiling.TypeCPU:
		if cfg.Mode == profiling.ModeOffCPU {
			return profiling.ProfileTypeOffCpuSample, nil
		}

		return profiling.ProfileTypeCpuSample, nil
	case profiling.TypeMemory:
		if cfg.Mode == profiling.ModePhysicalUsage {
			return profiling.ProfileTypePhysicalUsage, nil
		}

		return profiling.ProfileTypeMemSample, nil
	case profiling.TypeLock:
		return profiling.ProfileTypeLockTimeSample, nil
	default:
		return "", fmt.Errorf("unsupported profile type %q", cfg.Type)
	}
}

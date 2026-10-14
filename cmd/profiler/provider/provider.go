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

	"github.com/ccfos/huatuo/internal/profiler/aggregator"
	"github.com/ccfos/huatuo/internal/profiler/registry"
	"github.com/ccfos/huatuo/pkg/profiling"
)

// New binds a private configuration copy to a fresh collection task.
func New(implementation profiling.Implementation, config *Config) (*registry.ProfilerMeta, error) {
	cfg := *config
	cfg.PIDs = slices.Clone(config.PIDs)
	cfg.CPUIDs = slices.Clone(config.CPUIDs)
	meta := &registry.ProfilerMeta{ToolName: "profiler", Type: cfg.Type, Implementation: implementation}
	var err error
	switch {
	case implementation == profiling.ImplementationNative && cfg.Type == profiling.TypeCPU:
		meta.Description = "Native CPU profiler using eBPF"
		meta.Impl = &cpuNativeProfiler{config: cfg}
		meta.Aggregator, err = newNativeAggregator(&cfg)
	case implementation == profiling.ImplementationNative && cfg.Type == profiling.TypeMemory:
		mode, modeErr := resolveMemMode(cfg.Mode)
		if modeErr != nil {
			return nil, modeErr
		}
		if mode == profiling.ModePhysicalUsage {
			meta.AggregationMode = aggregator.AggregationModeOneShot
		}
		meta.Description = "Native memory profiler using eBPF"
		meta.Impl = &memNativeProfiler{config: cfg}
		meta.Aggregator, err = newNativeAggregator(&cfg)
	case implementation == profiling.ImplementationJava && cfg.Type == profiling.TypeCPU:
		meta.Description = "Java CPU profiler using async-profiler"
		meta.Impl = &cpuJavaProfiler{config: cfg}
		meta.AggregationMode = aggregator.AggregationModeOneShot
		meta.Aggregator, err = newJavaAggregator(&cfg)
	case implementation == profiling.ImplementationJava && cfg.Type == profiling.TypeMemory:
		meta.Description = "Java memory profiler using async-profiler"
		meta.Impl = &javaMemoryProfiler{config: cfg}
		meta.AggregationMode = aggregator.AggregationModeOneShot
		meta.Aggregator, err = newJavaAggregator(&cfg)
	case implementation == profiling.ImplementationPython && cfg.Type == profiling.TypeCPU:
		meta.Description = "Python CPU profiler using py-spy"
		meta.Impl = &pythonCPUProfiler{config: cfg}
		meta.AggregationMode = aggregator.AggregationModeOneShot
		meta.Aggregator, err = newPythonCPUAggregator(&cfg)
	default:
		return nil, fmt.Errorf("no profiler for implementation=%q type=%q", implementation, cfg.Type)
	}
	if err != nil {
		return nil, fmt.Errorf("create %s %s aggregator: %w", implementation, cfg.Type, err)
	}
	return meta, nil
}

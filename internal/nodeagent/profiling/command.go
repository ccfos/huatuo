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
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/ccfos/huatuo/internal/executil"
	"github.com/ccfos/huatuo/pkg/observation"
	profilingdomain "github.com/ccfos/huatuo/pkg/profiling"
)

// Bound all-CPU collection cost without changing direct CLI defaults.
const irqMaxEventsPerSecondPerCPU uint64 = 1000

func buildCommand(request *StartRequest, config *Config) (executil.Spec, error) {
	var command executil.Spec
	switch request.Spec.Type {
	case profilingdomain.TypeIRQ:
		command = buildIRQTracingCommand(request, config)
	case profilingdomain.TypeCPU, profilingdomain.TypeMemory:
		command = buildProfilerCommand(request, config)
	default:
		return executil.Spec{}, fmt.Errorf("unsupported profiling type %q", request.Spec.Type)
	}
	if request.Spec.HostCPUList != "" {
		command.Args = append(command.Args, "--cpu-list", request.Spec.HostCPUList)
	}
	return command, nil
}

func buildIRQTracingCommand(request *StartRequest, config *Config) executil.Spec {
	return executil.Spec{
		Path: filepath.Join(config.ToolBinDir, "irqtracing"),
		Args: []string{
			"--bpf-path", filepath.Join(config.BPFObjDir, "irqtracing.o"),
			"--duration", strconv.FormatInt(int64(request.Duration/time.Second), 10),
			"--max-events-per-second-per-cpu", strconv.FormatUint(irqMaxEventsPerSecondPerCPU, 10),
			"--output-format", "remote-store",
			"--output-path", config.ToolstreamSocketPath,
			"--task-id", request.RequestID,
		},
		MaxStdoutBytes: config.CommandOutputLimitBytes,
	}
}

func buildProfilerCommand(request *StartRequest, config *Config) executil.Spec {
	durationSeconds := int64(request.Duration / time.Second)
	aggregationInterval := min(config.AggregationInterval, request.Duration)
	if request.Spec.Language == profilingdomain.LanguagePython {
		aggregationInterval = request.Duration
	}

	args := []string{
		"--type", string(request.Spec.Type),
		"--language", string(request.Spec.Language),
		"--duration", strconv.FormatInt(durationSeconds, 10),
		"--aggr-interval", strconv.FormatInt(int64(aggregationInterval/time.Second), 10),
		"--max-concurrent-procs", strconv.Itoa(config.MaxConcurrentProcesses),
		"--output-format", "remote-store",
		"--output-path", config.ToolstreamSocketPath,
		"--task-id", request.RequestID,
		"--tool-path", config.ToolDir,
	}
	if request.Scope == observation.ScopeTypeContainer {
		args = append(args, "--container-id", request.ContainerID)
	}
	switch request.Spec.Type {
	case profilingdomain.TypeCPU:
		if request.Spec.Mode == profilingdomain.ModeOffCPU {
			args = append(args, "--cpu-mode", string(request.Spec.Mode))
		}
	case profilingdomain.TypeMemory:
		args = append(args, "--memory-mode", string(request.Spec.Mode))
	}
	if request.Spec.BinaryMatchPath != "" {
		args = append(args, "--binary-match-path", request.Spec.BinaryMatchPath)
	}

	return executil.Spec{
		Path:           filepath.Join(config.ToolBinDir, "profiler"),
		Args:           args,
		MaxStdoutBytes: config.CommandOutputLimitBytes,
	}
}

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
	"strconv"
	"time"

	"github.com/ccfos/huatuo/internal/exec"
	"github.com/ccfos/huatuo/pkg/observation"
	profilingdomain "github.com/ccfos/huatuo/pkg/profiling"
)

func buildCommand(request *StartRequest, config *Config) (exec.Spec, error) {
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
		"--output-format", "remote",
		"--output-storage", config.ToolstreamSocketPath,
		"--tracer-id", request.RequestID,
		"--huatuo-api-address", config.NodeAPIAddress,
	}
	if request.Scope == observation.ScopeContainer {
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
	switch request.Spec.Language {
	case profilingdomain.LanguageJava:
		args = append(args, "--tool-path", config.JavaToolPath)
	case profilingdomain.LanguagePython:
		args = append(args, "--tool-path", config.PythonToolPath)
	}

	return exec.Spec{
		Path:           config.ProfilerPath,
		Args:           args,
		MaxOutputBytes: config.CommandOutputLimitBytes,
	}, nil
}

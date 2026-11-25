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
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/ccfos/huatuo/internal/pod"
	"github.com/ccfos/huatuo/internal/profiling/toolpath"
	"github.com/ccfos/huatuo/internal/utils/cpuutil"
	"github.com/ccfos/huatuo/pkg/observation"
	profilingdomain "github.com/ccfos/huatuo/pkg/profiling"
)

func validateConfig(config *Config) error {
	if config == nil {
		return errors.New("create node profiling service: config is required")
	}
	switch {
	case config.ToolBinDir == "":
		return errors.New("create node profiling service: tool binary directory is required")
	case config.BPFObjDir == "":
		return errors.New("create node profiling service: BPF object directory is required")
	case config.ToolstreamSocketPath == "":
		return errors.New("create node profiling service: toolstream socket path is required")
	case config.AggregationInterval <= 0:
		return errors.New("create node profiling service: aggregation interval must be positive")
	case config.MaxConcurrentProcesses < 0:
		return errors.New("create node profiling service: maximum concurrent processes must not be negative")
	case config.CommandOutputLimitBytes <= 0:
		return errors.New("create node profiling service: command output limit must be positive")
	default:
		return nil
	}
}

func validateRequest(request *StartRequest) error {
	if request == nil {
		return fmt.Errorf("%w: request is required", ErrInvalidRequest)
	}
	if err := observation.ValidateScope(request.Scope, request.ContainerID); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	if err := request.Spec.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
	}
	if !profilingdomain.SupportsScope(request.Spec.Language, request.Spec.Type, request.Scope) {
		return fmt.Errorf(
			"%w: scope %q is not supported for type %q and language %q",
			ErrInvalidRequest,
			request.Scope,
			request.Spec.Type,
			request.Spec.Language,
		)
	}
	if request.ContainerID != "" {
		if err := pod.ValidateContainerID(request.ContainerID); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidRequest, err)
		}
	}
	return nil
}

func validateEnvironment(request *StartRequest, config *Config) error {
	cpuIDs, err := request.Spec.ParseHostCPUList()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrEnvironmentUnsupported, err)
	}
	if err := cpuutil.ValidateOnline(cpuIDs, cpuutil.SystemCPUOnlinePath); err != nil {
		return fmt.Errorf("%w: %w", ErrEnvironmentUnsupported, err)
	}

	if request.Spec.Type == profilingdomain.TypeIRQ {
		if err := validateExecutable(filepath.Join(config.ToolBinDir, "irqtracing")); err != nil {
			return fmt.Errorf("%w: %w", ErrEnvironmentUnsupported, err)
		}
		bpfPath := filepath.Join(config.BPFObjDir, "irqtracing.o")
		info, err := os.Stat(bpfPath)
		if err != nil {
			return fmt.Errorf("%w: inspect IRQ BPF object %q: %w", ErrEnvironmentUnsupported, bpfPath, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: IRQ BPF object %q is not a regular file", ErrEnvironmentUnsupported, bpfPath)
		}
		file, err := os.Open(bpfPath)
		if err != nil {
			return fmt.Errorf("%w: open IRQ BPF object %q: %w", ErrEnvironmentUnsupported, bpfPath, err)
		}
		if err := file.Close(); err != nil {
			return fmt.Errorf("%w: close IRQ BPF object %q: %w", ErrEnvironmentUnsupported, bpfPath, err)
		}
		return nil
	}

	if err := validateExecutable(filepath.Join(config.ToolBinDir, "profiler")); err != nil {
		return fmt.Errorf("%w: %w", ErrEnvironmentUnsupported, err)
	}
	if request.ContainerID != "" {
		container, err := pod.ContainerByID(request.ContainerID)
		if err != nil {
			return fmt.Errorf("%w: resolve container %q: %w", ErrEnvironmentUnsupported, request.ContainerID, err)
		}
		if container == nil {
			return fmt.Errorf("%w: container %q was not found", ErrEnvironmentUnsupported, request.ContainerID)
		}
	}

	if err := toolpath.Validate(request.Spec.Language, config.ToolDir); err != nil {
		return fmt.Errorf("%w: %w", ErrEnvironmentUnsupported, err)
	}
	return nil
}

func validateExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("executable %q is unavailable: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("executable %q is not a regular file", path)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("executable %q has no execute permission", path)
	}
	return nil
}

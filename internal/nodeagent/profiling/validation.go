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

	"huatuo-bamai/internal/pod"
	"huatuo-bamai/pkg/observation"
	profilingdomain "huatuo-bamai/pkg/profiling"
)

func validateConfig(config *Config) error {
	if config == nil {
		return errors.New("create node profiling service: config is required")
	}
	switch {
	case config.ProfilerPath == "":
		return errors.New("create node profiling service: profiler path is required")
	case config.ToolstreamSocketPath == "":
		return errors.New("create node profiling service: toolstream socket path is required")
	case config.NodeAPIAddress == "":
		return errors.New("create node profiling service: node API address is required")
	case config.AggregationInterval <= 0:
		return errors.New("create node profiling service: aggregation interval must be positive")
	case config.MaxConcurrentProcesses < 0:
		return errors.New("create node profiling service: maximum concurrent processes must not be negative")
	case config.CommandOutputLimitBytes <= 0:
		return errors.New("create node profiling service: command output limit must be positive")
	case config.ToolstreamServer == nil:
		return errors.New("create node profiling service: Toolstream server is required")
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
	if config.ResultPublisher == nil {
		return fmt.Errorf("%w: profiling result storage is not configured", ErrEnvironmentUnsupported)
	}
	if err := validateExecutable(config.ProfilerPath); err != nil {
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

	switch request.Spec.Language {
	case profilingdomain.LanguageJava:
		if config.JavaToolPath == "" {
			return fmt.Errorf("%w: Java tool path is not configured", ErrEnvironmentUnsupported)
		}
		if err := validateExecutable(filepath.Join(config.JavaToolPath, "bin", "asprof")); err != nil {
			return fmt.Errorf("%w: %w", ErrEnvironmentUnsupported, err)
		}
		if err := validateRegularFile(filepath.Join(config.JavaToolPath, "lib", "libasyncProfiler.so")); err != nil {
			return fmt.Errorf("%w: %w", ErrEnvironmentUnsupported, err)
		}
	case profilingdomain.LanguagePython:
		if config.PythonToolPath == "" {
			return fmt.Errorf("%w: Python tool path is not configured", ErrEnvironmentUnsupported)
		}
		if err := validateExecutable(filepath.Join(config.PythonToolPath, "py-spy")); err != nil {
			return fmt.Errorf("%w: %w", ErrEnvironmentUnsupported, err)
		}
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

func validateRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("file %q is unavailable: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("file %q is not a regular file", path)
	}
	return nil
}

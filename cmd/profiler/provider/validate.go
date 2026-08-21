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
	"os"
	"path/filepath"
	"strings"

	"huatuo-bamai/internal/process"
)

func validateResolvedPIDs(profilerName string, pids []int) error {
	if len(pids) == 0 {
		return fmt.Errorf("start %s profiler: no target processes found", profilerName)
	}
	return nil
}

func validateExpectedExecPath(pids []int, execPath string) error {
	if execPath == "" {
		return nil
	}
	for _, pid := range pids {
		actualPath, err := process.Executable(pid)
		if err != nil {
			return err
		}
		if actualPath != execPath {
			return fmt.Errorf("PID %d executable %q, want %q", pid, actualPath, execPath)
		}
	}
	return nil
}

func validateMaxProfilerProcesses(profilerName string, pids []int, maximum int) error {
	if maximum < 0 {
		return fmt.Errorf("start %s profiler: maximum profiler processes must not be negative", profilerName)
	}
	if maximum == 0 || len(pids) <= maximum {
		return nil
	}
	return fmt.Errorf(
		"start %s profiler: too many profiler processes: maximum=%d, required=%d",
		profilerName,
		maximum,
		len(pids),
	)
}

func validateToolFile(profilerName, toolPath, relativePath string, executable bool) error {
	path := filepath.Join(toolPath, relativePath)
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("start %s profiler: required tool %q is unavailable: %w", profilerName, path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("start %s profiler: required tool %q is not a regular file", profilerName, path)
	}
	if executable && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("start %s profiler: required tool %q is not executable", profilerName, path)
	}
	if !executable && info.Mode().Perm()&0o444 == 0 {
		return fmt.Errorf("start %s profiler: required tool %q is not readable", profilerName, path)
	}
	return nil
}

func validateProcessExecutables(profilerName, executablePrefix string, pids []int) error {
	for _, pid := range pids {
		path, err := process.Executable(pid)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(filepath.Base(path), executablePrefix) {
			return fmt.Errorf(
				"%s PID %d executable %q, want prefix %q",
				profilerName,
				pid,
				path,
				executablePrefix,
			)
		}
	}
	return nil
}

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

	"github.com/ccfos/huatuo/internal/process"
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
		// A running executable that was unlinked or replaced in place keeps
		// its path but gains the kernel's " (deleted)" marker. The marker
		// describes the file, not the process, so trim it from both sides
		// before deciding whether the target is still the requested binary.
		actual := process.TrimUnlinkedExecutable(actualPath)
		expected := process.TrimUnlinkedExecutable(execPath)
		if actual != expected {
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
		// ExecutableName drops the kernel's unlinked marker, so a target whose
		// binary was replaced in place is still recognized as its runtime.
		name, err := process.ExecutableName(pid)
		if err != nil {
			return err
		}
		if !hasExecutablePrefix(name, executablePrefix) {
			return fmt.Errorf(
				"%s PID %d executable name %q, want prefix %q",
				profilerName,
				pid,
				name,
				executablePrefix,
			)
		}
	}
	return nil
}

// hasExecutablePrefix reports whether name is the executable of the requested
// runtime. Callers pass a basename from process.ExecutableName, which already
// removed the kernel's unlinked marker.
func hasExecutablePrefix(name, prefix string) bool {
	if strings.HasPrefix(name, prefix) {
		return true
	}
	// RHEL 8 names its system Python executable platform-python.
	return prefix == "python" && strings.HasPrefix(name, "platform-python")
}

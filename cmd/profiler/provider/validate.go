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
		actualPath, err := process.ExecutablePath(pid)
		if err != nil {
			return err
		}
		// ExecutablePath only removes a marker that procfs identifies as the
		// kernel's unlinked-file suffix. The value supplied through
		// --binary-match-path stays literal, including a real filename that
		// ends in the same text.
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
// runtime. Callers pass a basename from process.ExecutableName, which removes
// the kernel's unlinked marker only when it does not belong to a literal name.
func hasExecutablePrefix(name, prefix string) bool {
	if strings.HasPrefix(name, prefix) {
		return true
	}
	// RHEL 8 names its system Python executable platform-python.
	return prefix == "python" && strings.HasPrefix(name, "platform-python")
}

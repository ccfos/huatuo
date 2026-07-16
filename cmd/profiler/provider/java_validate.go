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

	"huatuo-bamai/pkg/profiling"
)

func validateJavaFrequency(freq int) error {
	if freq < 1 || freq > 1000 {
		return fmt.Errorf("Java profiler frequency must be between 1 and 1000 samples per second")
	}
	return nil
}

func validateJavaToolPath(toolPath string) error {
	if err := validateToolFile("Java", toolPath, "bin/asprof", true); err != nil {
		return err
	}
	return validateToolFile("Java", toolPath, "lib/libasyncProfiler.so", false)
}

func validateJavaMemoryMode(mode profiling.MemoryMode) ([]string, error) {
	switch mode {
	case profiling.MemoryModeObjectAlloc:
		return []string{}, nil
	case profiling.MemoryModeObjectUsage:
		return []string{"--live"}, nil
	default:
		return nil, fmt.Errorf("unsupported Java memory mode %q", mode)
	}
}

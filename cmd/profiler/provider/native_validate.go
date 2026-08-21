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

func validateNativePIDs(profileType string, pids []int) error {
	if len(pids) > 1 {
		return fmt.Errorf("start native %s profiler: multiple PIDs are not supported", profileType)
	}
	return nil
}

func resolveMemMode(mode profiling.MemoryMode) (profiling.MemoryMode, error) {
	switch mode {
	case profiling.MemoryModeVirtualAlloc,
		profiling.MemoryModePhysicalUsage,
		profiling.MemoryModePhysicalAlloc:
		return mode, nil
	default:
		return profiling.MemoryModeUnknown, fmt.Errorf("invalid mode %q", mode)
	}
}

func resolveProbability(probability uint) (uint, error) {
	if probability < 1 || probability > 100 {
		return 0, fmt.Errorf("physical memory probability must be between 1 and 100")
	}

	return probability, nil
}

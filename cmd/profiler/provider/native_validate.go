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
	"strconv"
)

func validateNativePIDs(profileType string, pids []int) error {
	if len(pids) > 1 {
		return fmt.Errorf("start native %s profiler: multiple PIDs are not supported", profileType)
	}
	return nil
}

func validateNativeMemoryExtraFlags(mode string, flags map[string]string) error {
	supported := []string{"probability"}
	for key := range flags {
		if !slices.Contains(supported, key) {
			return fmt.Errorf("native memory profiler does not support --flags key %q", key)
		}
	}
	if mode == modeVirtualAlloc && flags["probability"] != "" {
		return fmt.Errorf("--flags probability is not supported by memory mode %q", mode)
	}
	return nil
}

func resolveMemMode(mode string) (string, error) {
	switch mode {
	case modeVirtualAlloc, modePhysicalUsage, modePhysicalAlloc:
		return mode, nil
	default:
		return "", fmt.Errorf("invalid mode %q", mode)
	}
}

func resolveProbability(probStr, internalMode string) (uint, error) {
	probability := uint64(100)

	if probStr != "" {
		prob, err := strconv.ParseUint(probStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid probability value %q: %w", probStr, err)
		}

		probability = prob
	}

	if probability < 1 || probability > 100 {
		return 0, fmt.Errorf("probability must be between 1 and 100")
	}

	return uint(probability), nil
}

func resolveScope(scope string) (bool, error) {
	switch scope {
	case "thread", "":
		return false, nil
	case "thread-group":
		return true, nil
	case "process-group":
		return false, fmt.Errorf("scope 'process-group' is not supported by mem profiler")
	default:
		return false, fmt.Errorf("unsupported scope for mem profiler: %q", scope)
	}
}

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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateMaxProfilerProcesses(t *testing.T) {
	tests := []struct {
		name         string
		profilerName string
		pids         []int
		maximum      int
		wantError    string
	}{
		{name: "unlimited", profilerName: "Java", pids: []int{1, 2}},
		{
			name:         "negative maximum",
			profilerName: "Java",
			pids:         []int{1, 2},
			maximum:      -1,
			wantError:    "start Java profiler: maximum profiler processes must not be negative",
		},
		{name: "within maximum", profilerName: "Python", pids: []int{1, 2}, maximum: 2},
		{
			name:         "over limit",
			profilerName: "Python",
			pids:         []int{1, 2},
			maximum:      1,
			wantError:    "start Python profiler: too many profiler processes: maximum=1, required=2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMaxProfilerProcesses(tt.profilerName, tt.pids, tt.maximum)
			if tt.wantError != "" {
				require.EqualError(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateResolvedPIDs(t *testing.T) {
	require.NoError(t, validateResolvedPIDs("Java", []int{1}))
	require.EqualError(t, validateResolvedPIDs("Java", nil), "start Java profiler: no target processes found")
}

func TestValidateToolFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool")
	require.NoError(t, os.WriteFile(path, []byte("tool"), 0o600))
	require.EqualError(
		t,
		validateToolFile("Python", dir, "tool", true),
		fmt.Sprintf("start Python profiler: required tool %q is not executable", path),
	)
	require.NoError(t, os.Chmod(path, 0o755))
	require.NoError(t, validateToolFile("Python", dir, "tool", true))
	require.NoError(t, os.Chmod(path, 0o000))
	require.EqualError(
		t,
		validateToolFile("Java", dir, "tool", false),
		fmt.Sprintf("start Java profiler: required tool %q is not readable", path),
	)
}

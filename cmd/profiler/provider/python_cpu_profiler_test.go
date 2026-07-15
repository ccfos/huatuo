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
	"testing"

	pcontext "huatuo-bamai/internal/profiler/context"

	"github.com/stretchr/testify/require"
)

func TestResolvePythonPidsExplicitTargets(t *testing.T) {
	pctx := &pcontext.ProfilerContext{PIDs: []int{123, 456}}

	pids, err := resolvePythonPids(pctx)
	require.NoError(t, err)
	require.Equal(t, []int{123, 456}, pids)
}

func TestResolvePythonPidsToolLimit(t *testing.T) {
	err := validatePythonToolLimit([]int{123, 456}, 1)
	require.EqualError(t, err, "sampling failed: too many target Python processes (limit: 1, found: 2)")
}

func TestPythonRootPids(t *testing.T) {
	parents := map[int]int{
		100: 1,
		101: 100,
		102: 101,
		200: 1,
		201: 200,
	}
	parentPID := func(pid int) (int, error) {
		return parents[pid], nil
	}

	roots, err := pythonRootPids([]int{100, 101, 102, 200, 201}, parentPID)
	require.NoError(t, err)
	require.Equal(t, []int{100, 200}, roots)
}

func TestPythonRootPidsDetectsParentCycle(t *testing.T) {
	parents := map[int]int{100: 200, 200: 100}
	parentPID := func(pid int) (int, error) {
		return parents[pid], nil
	}

	_, err := pythonRootPids([]int{100}, parentPID)
	require.EqualError(t, err, "resolve Python target PID 100 ancestry: process parent cycle at PID 100")
}

func TestBuildPySpyArgs(t *testing.T) {
	require.Equal(t, []string{
		"record",
		"-d", "10",
		"-f", "raw",
		"-r", "99",
		"--subprocesses",
		"-o", "/dev/stdout",
		"-p", "123",
	}, buildPySpyArgs(123, "10", "99"))
}

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
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateJavaFrequency(t *testing.T) {
	require.NoError(t, validateJavaFrequency(1))
	require.NoError(t, validateJavaFrequency(1000))
	require.Error(t, validateJavaFrequency(0))
	require.Error(t, validateJavaFrequency(1001))
}

func TestValidateJavaToolPath(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "bin"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "lib"), 0o755))
	asprof := filepath.Join(dir, "bin/asprof")
	require.NoError(t, os.WriteFile(asprof, []byte("tool"), 0o600))
	require.NoError(t, os.Chmod(asprof, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "lib/libasyncProfiler.so"), []byte("lib"), 0o600))
	require.NoError(t, validateJavaToolPath(dir))
}

func TestValidateJavaMemoryMode(t *testing.T) {
	version := func(int) (int, error) { return 17, nil }
	args, err := validateJavaMemoryMode(javaMemoryModeObjectAlloc, []int{1}, version)
	require.NoError(t, err)
	require.Empty(t, args)

	args, err = validateJavaMemoryMode(javaMemoryModeObjectUsage, []int{1}, version)
	require.NoError(t, err)
	require.Equal(t, []string{"--live"}, args)

	_, err = validateJavaMemoryMode(javaMemoryModeObjectUsage, []int{1}, func(int) (int, error) { return 8, nil })
	require.EqualError(t, err, "object_usage mode requires Java 11 or newer: PID 1 uses Java 8")

	_, err = validateJavaMemoryMode(javaMemoryModeObjectUsage, []int{1}, func(int) (int, error) {
		return 0, errors.New("unavailable")
	})
	require.EqualError(t, err, "failed to get Java version for PID 1: unavailable")

	_, err = validateJavaMemoryMode("unknown", []int{1}, version)
	require.EqualError(t, err, `unsupported Java memory mode "unknown"`)
}

//go:build linux

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

package build

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestImageHelperPreservesWorkspaceArguments(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is required to execute the image helper")
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required to discover the helper's workspace")
	}
	helper, err := filepath.Abs("build-run-testing-image.sh")
	require.NoError(t, err)
	for _, name := range []string{"checkout", "checkout with spaces"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			workspace := filepath.Join(dir, name)
			output, err := exec.CommandContext(t.Context(), git, "init", "--quiet", workspace).CombinedOutput()
			require.NoError(t, err, "%s", output)
			docker := filepath.Join(dir, "docker")
			require.NoError(t, os.WriteFile(docker,
				[]byte("#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$HUATUO_DOCKER_ARGS/$1\"\n"), 0o600))
			require.NoError(t, os.Chmod(docker, 0o700))
			t.Setenv("HUATUO_DOCKER_ARGS", dir)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			cmd := exec.CommandContext(t.Context(), bash, helper)
			cmd.Dir = workspace
			output, err = cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			arguments := func(command string) []string {
				raw, err := os.ReadFile(filepath.Join(dir, command))
				require.NoError(t, err)
				return strings.Split(strings.TrimSuffix(string(raw), "\x00"), "\x00")
			}
			build := arguments("build")
			require.Equal(t, workspace, build[len(build)-1], "Docker build context")
			fileFlag := slices.Index(build, "-f")
			require.NotEqual(t, -1, fileFlag)
			require.Less(t, fileFlag+1, len(build))
			require.Equal(t, filepath.Join(workspace, "Dockerfile.devel"), build[fileFlag+1])
			run := arguments("run")
			volumeFlag := slices.Index(run, "-v")
			require.NotEqual(t, -1, volumeFlag)
			require.Less(t, volumeFlag+1, len(run))
			require.Equal(t, workspace+":/workspace", run[volumeFlag+1], "Docker workspace bind mount")
		})
	}
}

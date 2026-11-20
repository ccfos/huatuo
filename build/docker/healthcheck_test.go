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

package docker

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestElasticsearchHealthcheckCredentials(t *testing.T) {
	data, err := os.ReadFile("docker-compose.yml")
	require.NoError(t, err)
	var compose struct {
		Services map[string]struct {
			Healthcheck struct{ Test []string }
		}
	}
	require.NoError(t, yaml.Unmarshal(data, &compose))
	command := compose.Services["elasticsearch"].Healthcheck.Test
	require.Len(t, command, 2)
	require.Equal(t, "CMD-SHELL", command[0])
	// Compose's $$ escape leaves expansion to the container shell.
	script := strings.ReplaceAll(command[1], "$$", "$")
	for _, password := range []string{"", "plain", "two words", "secret*", "a\tb", `a$'";b`} {
		t.Run(password, func(t *testing.T) {
			dir := t.TempDir()
			capture := filepath.Join(dir, "arguments")
			curl := filepath.Join(dir, "curl")
			require.NoError(t, os.WriteFile(curl,
				[]byte("#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$CAPTURE\"\n"), 0o600))
			require.NoError(t, os.Chmod(curl, 0o700))
			// An unquoted credential can also expand against local filenames.
			require.NoError(t, os.WriteFile(filepath.Join(dir, "elastic:secret-expanded"), nil, 0o600))
			t.Setenv("ELASTIC_PASSWORD", password)
			t.Setenv("CAPTURE", capture)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", script)
			cmd.Dir = dir
			output, err := cmd.CombinedOutput()
			require.NoError(t, err, "%s", output)
			arguments, err := os.ReadFile(capture)
			require.NoError(t, err)
			require.Equal(t, []string{"-fsS", "-u", "elastic:" + password, "http://localhost:9200/", ""},
				strings.Split(string(arguments), "\x00"))
		})
	}
}

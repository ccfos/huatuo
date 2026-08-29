// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Fixture and runner tests exercise the integration scripts through real
// commands, with device operations replaced at their dependency boundary.
package iocost

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/pelletier/go-toml"
	"github.com/stretchr/testify/require"
)

func TestIOCostGenericIntegrationBlacklists(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repository := filepath.Join(filepath.Dir(file), "..", "..")
	configScript := filepath.Join(repository, "integration", "config.sh")
	for _, writer := range []string{
		"write_default_config",
		"write_include_filter_config",
		"write_exclude_filter_config",
		"write_sched_tick_config",
		"write_sched_tick_irqoff_config",
	} {
		t.Run(writer, func(t *testing.T) {
			directory := t.TempDir()
			command := exec.CommandContext(t.Context(), "bash", "-c", `
set -euo pipefail
HUATUO_BAMAI_TEST_TMPDIR=$2
source "$1"
"$3"
`, "iocost-generic-integration-config", configScript, directory, writer)
			output, err := command.CombinedOutput()
			require.NoError(t, err, "%s", output)
			data, err := os.ReadFile(filepath.Join(directory, "bamai.conf"))
			require.NoError(t, err)
			var config struct {
				BlackList []string
			}
			require.NoError(t, toml.Unmarshal(data, &config))
			count := 0
			for _, name := range config.BlackList {
				if name == "iocost" {
					count++
				}
			}
			require.Equal(t, 1, count,
				"ordinary integration config must disable IOCOST exactly once")
			if writer == "write_sched_tick_config" || writer == "write_sched_tick_irqoff_config" {
				require.Contains(t, config.BlackList, "blk_throtl")
				require.Contains(t, config.BlackList, "io_health")
				require.NotContains(t, config.BlackList, "sched_tick")
				require.NotContains(t, config.BlackList, "tracing_status")
			}
		})
	}
}

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

package autotracing

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	internalconfig "github.com/ccfos/huatuo/internal/config"
	"github.com/ccfos/huatuo/internal/profiling/flamegraph"
	"github.com/ccfos/huatuo/internal/toolstream"
)

func TestRunProfileCommand(t *testing.T) {
	originalCoreBinDir := internalconfig.CoreBinDir
	t.Cleanup(func() { internalconfig.CoreBinDir = originalCoreBinDir })

	tests := []struct {
		name             string
		tool             string
		containerID      string
		script           string
		wantLabel        string
		wantError        string
		wantErrorMissing string
	}{
		{
			name:      "perf host",
			tool:      cpuToolPerf,
			script:    `printf '%s\n' '[{"level":0,"value":1,"self":1,"label":"host"}]'`,
			wantLabel: "host",
		},
		{
			name:        "perf container",
			tool:        cpuToolPerf,
			containerID: "0123456789ab",
			script:      `printf '%s\n' '[{"level":0,"value":1,"self":1,"label":"container"}]'`,
			wantLabel:   "container",
		},
		{
			name: "perf complete output exceeds capture limit",
			tool: cpuToolPerf,
			script: `printf '[{"level":0,"value":1,"self":1,"label":"'
head -c 70000 /dev/zero | tr '\000' x
printf '"}]\n'`,
			wantLabel: strings.Repeat("x", 70000),
		},
		{
			name:      "perf empty output",
			tool:      cpuToolPerf,
			script:    "exit 0",
			wantError: "run system-wide perf: empty output",
		},
		{
			name:      "perf invalid JSON",
			tool:      cpuToolPerf,
			script:    "printf not-json",
			wantError: "decode flame data",
		},
		{
			name:        "perf failure retains stderr tail",
			tool:        cpuToolPerf,
			containerID: "0123456789ab",
			script: `printf 'stderr-start\n' >&2
head -c 5000 /dev/zero | tr '\000' x >&2
printf 'stderr-tail\n' >&2
exit 2`,
			wantError:        "output=\"" + strings.Repeat("x", (4<<10)-len("stderr-tail\n")) + "stderr-tail\"",
			wantErrorMissing: "stderr-start",
		},
		{
			name:             "perf ignores partial stdout on failure",
			tool:             cpuToolPerf,
			script:           "printf 'partial result'; printf 'perf failed' >&2; exit 2",
			wantError:        "perf failed",
			wantErrorMissing: "partial result",
		},
		{
			name:   "profiler host",
			tool:   cpuToolProfiler,
			script: "exit 0",
		},
		{
			name:        "profiler container",
			tool:        cpuToolProfiler,
			containerID: "0123456789ab",
			script:      "exit 0",
		},
		{
			name:      "profiler stdout diagnostics",
			tool:      cpuToolProfiler,
			script:    "printf 'profile publication failed'; exit 2",
			wantError: "profile publication failed",
		},
		{
			name:      "profiler stderr diagnostics",
			tool:      cpuToolProfiler,
			script:    "printf 'profile setup failed' >&2; exit 2",
			wantError: "profile setup failed",
		},
		{
			name: "profiler retains bounded stdout",
			tool: cpuToolProfiler,
			script: `printf 'stdout-start\n'
head -c 5000 /dev/zero | tr '\000' x
printf 'stdout-tail\n'
exit 2`,
			wantError:        "stdout-tail",
			wantErrorMissing: "stdout-start",
		},
		{
			name:      "unknown tool",
			tool:      "unknown",
			script:    "exit 0",
			wantError: "unsupported CPU profiling tool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			directory := t.TempDir()
			internalconfig.CoreBinDir = directory
			callsPath := filepath.Join(directory, "calls")
			t.Setenv("PROFILE_CALLS", callsPath)
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$PROFILE_CALLS\"\n" + tt.script + "\n"
			toolPath := filepath.Join(directory, tt.tool)
			if err := os.WriteFile(toolPath, []byte(script), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(toolPath, 0o700); err != nil {
				t.Fatal(err)
			}
			result, err := runProfileCommand(t.Context(), profileRequest{
				tool:        tt.tool,
				duration:    3 * time.Second,
				containerID: tt.containerID,
			})
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("runProfileCommand() error = %v, want %q", err, tt.wantError)
				}
				if tt.wantErrorMissing != "" && strings.Contains(err.Error(), tt.wantErrorMissing) {
					t.Fatalf("error contains unwanted diagnostics %q", tt.wantErrorMissing)
				}
				if result.taskID != "" || result.flameData != nil {
					t.Fatalf("failed command returned a result: %+v", result)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}

			var wantArgs []string
			if tt.tool == cpuToolPerf {
				wantArgs = []string{"--bpf-path", filepath.Join(internalconfig.CoreBpfDir, "perf.o"), "--duration", "3"}
				wantFrames := []flamegraph.FrameData{
					{
						Level: 0,
						Value: 1,
						Self:  1,
						Label: tt.wantLabel,
					},
				}
				if diff := cmp.Diff(wantFrames, result.flameData); diff != "" {
					t.Fatalf("flame data mismatch (-want +got):\n%s", diff)
				}
				if result.taskID != "" {
					t.Fatalf("perf task id = %q, want empty", result.taskID)
				}
			} else {
				if result.taskID == "" || result.flameData != nil {
					t.Fatalf("profiler result = %+v", result)
				}
				wantArgs = []string{
					"--type", "cpu", "--language", "c", "--cpu-mode", "oncpu",
					"--duration", "3", "--aggr-interval", "3",
					"--output-format", "remote-store", "--output-path", toolstream.DefaultSockPath,
					"--task-id", result.taskID,
				}
			}
			if tt.containerID != "" {
				wantArgs = append(wantArgs, "--container-id", tt.containerID)
			}
			calls, err := os.ReadFile(callsPath)
			if err != nil {
				t.Fatal(err)
			}
			gotArgs := strings.Split(strings.TrimSpace(string(calls)), "\n")
			if diff := cmp.Diff(wantArgs, gotArgs); diff != "" {
				t.Fatalf("arguments mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRunProfileCommandCancellation(t *testing.T) {
	originalCoreBinDir := internalconfig.CoreBinDir
	t.Cleanup(func() { internalconfig.CoreBinDir = originalCoreBinDir })
	for _, tool := range []string{cpuToolPerf, cpuToolProfiler} {
		t.Run(tool, func(t *testing.T) {
			internalconfig.CoreBinDir = t.TempDir()
			toolPath := filepath.Join(internalconfig.CoreBinDir, tool)
			if err := os.WriteFile(toolPath, []byte("#!/bin/sh\nexec sleep 30\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(toolPath, 0o700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel()
			_, err := runProfileCommand(ctx, profileRequest{
				tool:     tool,
				duration: time.Second,
			})
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error = %v, want DeadlineExceeded", err)
			}
		})
	}
}

func TestProfileToolConfiguration(t *testing.T) {
	for _, section := range []string{"CPUIdle", "CPUSys"} {
		for _, value := range []string{"", "perf", "profiler", "unknown"} {
			t.Run(section+"/"+strconv.Quote(value), func(t *testing.T) {
				cfg := &Config{}
				path := filepath.Join(t.TempDir(), "bamai.conf")
				if err := os.WriteFile(path, []byte("["+section+"]\nTool = "+strconv.Quote(value)+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := internalconfig.Load(path, cfg); err != nil {
					t.Fatal(err)
				}
				err := cfg.Validate()
				if value == "perf" || value == "profiler" {
					if err != nil {
						t.Fatal(err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "AutoTracing."+section+".Tool") {
					t.Fatalf("Validate() error = %v, want invalid tool", err)
				}
			})
		}
	}
	path := filepath.Join(t.TempDir(), "bamai.conf")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{}
	if err := internalconfig.Load(path, cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.CPUIdle.Tool != cpuToolPerf || cfg.CPUSys.Tool != cpuToolPerf {
		t.Fatalf("default tools: cpuidle=%q cpusys=%q", cfg.CPUIdle.Tool, cfg.CPUSys.Tool)
	}
}

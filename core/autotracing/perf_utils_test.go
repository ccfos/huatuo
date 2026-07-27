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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"huatuo-bamai/pkg/tracing"
)

func TestRunPerfCommand(t *testing.T) {
	originalTaskBinDir := tracing.TaskBinDir
	t.Cleanup(func() {
		tracing.TaskBinDir = originalTaskBinDir
	})

	tests := []struct {
		name        string
		script      string
		request     perfRequest
		wantOutput  []string
		wantError   string
		wantMissing string
	}{
		{
			name:   "system wide",
			script: "#!/bin/sh\nprintf '%s\\n' \"$@\"\n",
			request: perfRequest{
				duration: 7 * time.Second,
			},
			wantOutput:  []string{"--bpf-path", "--duration", "7"},
			wantMissing: "--container-id",
		},
		{
			name:   "container",
			script: "#!/bin/sh\nprintf '%s\\n' \"$@\"\n",
			request: perfRequest{
				duration:    3 * time.Second,
				containerID: "0123456789ab",
			},
			wantOutput: []string{
				"--container-id",
				"0123456789ab",
				"--duration",
				"3",
			},
		},
		{
			name: "empty output",
			script: `#!/bin/sh
exit 0
`,
			request: perfRequest{
				duration: time.Second,
			},
			wantError: "run system-wide perf: empty output",
		},
		{
			name: "failed command truncates diagnostics",
			script: `#!/bin/sh
head -c 5000 /dev/zero | tr '\000' x
exit 2
`,
			request: perfRequest{
				duration:    time.Second,
				containerID: "0123456789ab",
			},
			wantError: "(truncated)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			taskBinDir := t.TempDir()
			tracing.TaskBinDir = taskBinDir
			if err := os.WriteFile(
				filepath.Join(taskBinDir, "perf"),
				[]byte(tt.script),
				0o600,
			); err != nil {
				t.Fatalf("os.WriteFile() error = %v", err)
			}
			if err := os.Chmod(filepath.Join(taskBinDir, "perf"), 0o700); err != nil {
				t.Fatalf("os.Chmod() error = %v", err)
			}

			output, err := runPerfCommand(t.Context(), tt.request)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("runPerfCommand() error = %v, want contain %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("runPerfCommand() error = %v", err)
			}

			actual := string(output)
			for _, expected := range tt.wantOutput {
				if !strings.Contains(actual, expected+"\n") {
					t.Errorf("runPerfCommand() output = %q, want contain %q", actual, expected)
				}
			}
			if tt.wantMissing != "" && strings.Contains(actual, tt.wantMissing) {
				t.Errorf("runPerfCommand() output = %q, want exclude %q", actual, tt.wantMissing)
			}
		})
	}
}

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

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDaemonLogCheck(t *testing.T) {
	script, err := os.ReadFile("../../test_autotracing_cpuidle.sh")
	if err != nil {
		t.Fatal(err)
	}
	start := bytes.Index(script, []byte("cpuidle_log_check() {\n"))
	if start < 0 {
		t.Fatal("cpuidle log check function is missing")
	}
	function := string(script[start:])
	end := strings.Index(function, "\n}\n")
	if end < 0 {
		t.Fatal("cpuidle log check function is unterminated")
	}
	function = function[:end+3]
	t.Setenv("HUATUO_BAMAI_MATCH_KEYWORDS", `level="?(error|panic|fatal)"?|panic:`)
	const loss = `level="warning" msg="lost BPF perf event samples" func="internal/pod.cgroupCssEventSyncHandler.func1"`
	for _, tc := range []struct {
		name, log string
		valid     bool
	}{
		{"info", `level="info" msg="ready"`, true},
		{"discovery", `level="warning" msg="creation is not yet reflected by kubelet"`, true},
		{"CSS refresh", loss, true},
		{"other sample loss", strings.ReplaceAll(loss, "cgroupCssEventSyncHandler", "otherHandler"), false},
		{"unknown warning", `level="warning" msg="unexpected"`, false},
		{"error", `level="error" msg="profiling failed"`, false},
		{"panic", `panic: test failure`, false},
		{"many warnings", strings.Repeat("level=warning msg=unexpected\n", 10000), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "huatuo.log"), []byte(tc.log+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", "set -euo pipefail\nsource ../../../integration/lib.sh\n"+function+"\ncpuidle_log_check")
			cmd.Env = append(os.Environ(), "HUATUO_BAMAI_TEST_TMPDIR="+dir)
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.valid {
				t.Fatalf("log check error=%v, want valid=%t; output=%s", err, tc.valid, output)
			}
		})
	}
}

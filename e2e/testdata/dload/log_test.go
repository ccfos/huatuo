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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestDloadLogCheck(t *testing.T) {
	data, err := os.ReadFile("../../test_autotracing_dload.sh")
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the script's actual whitelist without starting Pods or devices.
	function := regexp.MustCompile(`(?s)dload_log_check\(\) \{\n.*?\n\}`).Find(data)
	if len(function) == 0 {
		t.Fatal("dload_log_check function not found in the E2E script")
	}
	script := "source ../../../integration/lib.sh\n" +
		"HUATUO_BAMAI_TEST_TMPDIR=$1\n" +
		string(function) + "\ndload_log_check\n"
	t.Setenv("HUATUO_BAMAI_MATCH_KEYWORDS", `level="?(error|panic|fatal)"?|panic:`)
	const cssLoss = `level="warning" msg="lost BPF perf event samples" error="bpf: 6 perf event samples lost" file="container_css_default.go:163" func="internal/pod.cgroupCssEventSyncHandler.func1"`
	const creation = `level="warning" msg="refresh container view" error="container creation is not yet reflected by kubelet"`
	for _, tc := range []struct {
		name  string
		log   string
		valid bool
	}{
		{"info", `level="info" msg="dload threshold exceeded"`, true},
		{"creation hint", creation, true},
		{"CSS perf loss", cssLoss, true},
		{"both recoverable warnings", creation + "\n" + cssLoss, true},
		{"other perf channel", strings.ReplaceAll(cssLoss, "internal/pod.cgroupCssEventSyncHandler.func1", "internal/bpf.(*BpfDbg).debugEventLoop"), false},
		{"unknown perf source", `level="warning" msg="lost BPF perf event samples"`, false},
		{"different CSS warning", strings.ReplaceAll(cssLoss, "lost BPF perf event samples", "failed to read CSS events"), false},
		{"unexpected warning", `level="warning" msg="unexpected warning"`, false},
		{"warning after recoverable loss", cssLoss + "\nlevel=warning msg=unexpected", false},
		{"error", `level=error msg=failed`, false},
		{"quoted error", `level="error" msg="failed"`, false},
		{"JSON error", `{"level":"error","msg":"failed"}`, false},
		{"CSS error is not exempt", strings.ReplaceAll(cssLoss, `level="warning"`, `level="error"`), false},
		{"panic", "panic: failed", false},
		{"fatal", `level="fatal" msg="failed"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "huatuo.log"), []byte(tc.log+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command("bash", "-c", script, "dload-log-check", dir).CombinedOutput()
			if (err == nil) != tc.valid {
				t.Fatalf("dload_log_check() error = %v, valid = %t; output: %s", err, tc.valid, output)
			}
		})
	}
	t.Run("missing log", func(t *testing.T) {
		output, err := exec.Command("bash", "-c", script, "dload-log-check", t.TempDir()).CombinedOutput()
		if err == nil || !strings.Contains(string(output), "log is not readable") {
			t.Fatalf("dload_log_check() error = %v, want unreadable log; output: %s", err, output)
		}
	})
}

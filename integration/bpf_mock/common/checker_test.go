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

//go:build integration

package bpfmock

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckerWithoutGo(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []string{"direct", "elasticsearch"} {
		for _, tc := range []struct {
			name, state, wantMessage string
			override                 bool
			wantExit                 int
		}{
			{name: "packaged checker"},
			{name: "explicit override", override: true},
			{name: "missing packaged checker", state: "missing", wantExit: 1, wantMessage: "run make build"},
			{name: "non-executable packaged checker", state: "not-executable", wantExit: 1, wantMessage: "not executable"},
			{name: "missing override", override: true, state: "missing", wantExit: 1, wantMessage: "not executable"},
			{name: "non-executable override", override: true, state: "not-executable", wantExit: 1, wantMessage: "not executable"},
			{name: "checker rejects output", state: "reject", wantExit: 1, wantMessage: "verification failed"},
		} {
			t.Run(entry+"/"+tc.name, func(t *testing.T) {
				workspace := t.TempDir()
				packageDir := filepath.Join(workspace, "package")
				checker := filepath.Join(packageDir, "_output", "tests", "memory_oom_kill-mock-checker")
				if err := os.MkdirAll(filepath.Dir(checker), 0o700); err != nil {
					t.Fatal(err)
				}
				override := ""
				if tc.override {
					// A valid packaged checker must not hide an invalid override.
					if err := os.WriteFile(checker, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
						t.Fatal(err)
					}
					checker = filepath.Join(workspace, "checker override")
					override = checker
				}
				if tc.state != "missing" {
					script := `#!/bin/sh
if [ "$CHECKER_ENTRY" = elasticsearch ]; then
 [ "$1" = "$HUATUO_BAMAI_TEST_TMPDIR/events.json" ] && [ -f "$1" ] || exit 91
 shift
fi
[ "$#" = 2 ] && [ "$1" = --fixture ] && [ "$2" = 'argument with spaces' ] || exit 92
[ "$CHECKER_STATE" != reject ] || { echo 'fixture rejected'; exit 42; }
echo 'fixture verified'
`
					mode := os.FileMode(0o700)
					if tc.state == "not-executable" {
						mode = 0o600
					}
					if err := os.WriteFile(checker, []byte(script), mode); err != nil {
						t.Fatal(err)
					}
				}
				cmd := exec.Command("bash", "-c", `
set -euo pipefail
source "$ROOT_DIR/integration/bpf_mock/common/lib.sh"
ROOT_DIR=$TEST_PACKAGE_DIR
command() {
 if [[ $* == '-v go' ]]; then return 1; fi
 builtin command "$@"
}
go() { echo 'unexpected runtime compilation' >&2; exit 90; }
# Exercise checker dispatch without an Elasticsearch service or a polling delay.
read_bpf_mock_es_events() { printf '{}\n' > "$1"; }
wait_until() { shift 2; "$@"; }
if [[ $CHECKER_ENTRY == elasticsearch ]]; then
 verify_bpf_mock_es_events memory_oom_kill "$HUATUO_BAMAI_TEST_TMPDIR/events.json" --fixture 'argument with spaces'
else
 run_bpf_mock_checker memory_oom_kill --fixture 'argument with spaces'
fi
`)
				cmd.Env = append(os.Environ(), "ROOT_DIR="+root, "HUATUO_BAMAI_TEST_TMPDIR="+workspace,
					"TEST_PACKAGE_DIR="+packageDir, "BPF_MOCK_CHECKER_BIN="+override,
					"CHECKER_ENTRY="+entry, "CHECKER_STATE="+tc.state)
				output, err := cmd.CombinedOutput()
				if tc.wantExit == 0 {
					if err != nil || !strings.Contains(string(output), "fixture verified") {
						t.Fatalf("prebuilt checker: %v: %s", err, output)
					}
				} else {
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != tc.wantExit ||
						!strings.Contains(string(output), tc.wantMessage) {
						t.Fatalf("want exit %d and %q: %v: %s", tc.wantExit, tc.wantMessage, err, output)
					}
					if tc.state == "reject" {
						log, err := os.ReadFile(filepath.Join(workspace, "mock-event-checker.log"))
						if err != nil || !strings.Contains(string(log), "fixture rejected") {
							t.Fatalf("missing checker failure diagnostic: %v: %s", err, log)
						}
					}
				}
			})
		}
	}
}

func TestMockTestDiscovery(t *testing.T) {
	core, err := filepath.Abs("core.sh")
	if err != nil {
		t.Fatal(err)
	}
	for script, wantMatch := range map[string]bool{
		"test_events_new_feature_mock.sh":                        true,
		"/integration/test_events_new_feature_mock_container.sh": true,
		"test_metrics_new_feature_mock.sh":                       true,
		"test_metrics_new_feature_mock_container.sh":             true,
		"test_events_memory_oom_kill.sh":                         false,
		"test_metrics.sh":                                        false,
		"test_events_new_feature_mock.sh.bak":                    false,
		"new_feature_mock.sh":                                    false,
	} {
		t.Run(script, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", `source "$1"; is_bpf_mock_test "$2"`, "test", core, script)
			output, err := cmd.CombinedOutput()
			if wantMatch {
				if err != nil {
					t.Fatalf("mock test not discovered: %v: %s", err, output)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 1 {
					t.Fatalf("non-mock test matched: %v: %s", err, output)
				}
			}
		})
	}
}

func TestMockAttachmentReadiness(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, log string
		success   bool
	}{
		{"ready", `msg="attached BPF program" attach_type="kprobe" symbol="hook_mock"
msg="attached BPF and created event pipe" map_name="events"`, true},
		{"wrong type", `msg="attached BPF program" attach_type="kretprobe" symbol="hook_mock"`, false},
		{"wrong symbol", `msg="attached BPF program" attach_type="kprobe" symbol="other_mock"`, false},
		{"no event pipe", `msg="attached BPF program" attach_type="kprobe" symbol="hook_mock"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "huatuo.log"), []byte(tc.log), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("bash", "-c", `
set -euo pipefail
source "$ROOT_DIR/integration/bpf_mock/common/lib.sh"
wait_until() { shift 2; "$@"; }
await_bpf_mock_attached events kprobe:hook_mock
`)
			cmd.Env = append(os.Environ(), "ROOT_DIR="+root, "HUATUO_BAMAI_TEST_TMPDIR="+workspace)
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.success {
				t.Fatalf("attachment readiness: %v: %s", err, out)
			}
		})
	}
}

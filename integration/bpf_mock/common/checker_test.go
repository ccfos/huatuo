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
	for _, tc := range []struct {
		name     string
		prebuilt bool
		wantExit int
	}{
		{name: "missing Go", wantExit: 77},
		{name: "prebuilt checker", prebuilt: true},
		{name: "invalid prebuilt checker", prebuilt: true, wantExit: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			checker := ""
			if tc.prebuilt {
				checker = filepath.Join(workspace, "checker")
				if tc.wantExit == 0 {
					if err := os.WriteFile(checker, []byte("#!/bin/sh\n[ \"$1\" = --fixture ]\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(checker, 0o700); err != nil {
						t.Fatal(err)
					}
				}
			}
			cmd := exec.Command("bash", "-c", `
set -euo pipefail
source "$ROOT_DIR/integration/bpf_mock/common/lib.sh"
command() {
 if [[ $* == '-v go' ]]; then return 1; fi
 builtin command "$@"
}

run_bpf_mock_checker memory_oom_kill --fixture
`)
			cmd.Env = append(os.Environ(), "ROOT_DIR="+root, "HUATUO_BAMAI_TEST_TMPDIR="+workspace, "BPF_MOCK_CHECKER_BIN="+checker)
			output, err := cmd.CombinedOutput()
			if tc.wantExit == 0 {
				if err != nil {
					t.Fatalf("prebuilt checker failed: %v: %s", err, output)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != tc.wantExit {
					t.Fatalf("want exit %d: %v: %s", tc.wantExit, err, output)
				}
			}
		})
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

func TestCheckerBuildReuse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		wantBuilds int
		wantExit   int
	}{
		{name: "private", wantBuilds: 2},
		{name: "shared", wantBuilds: 1},
		{name: "removed", wantBuilds: 3},
		{name: "build_failure", wantBuilds: 1, wantExit: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			cmd := exec.Command("bash", "-c", `
set -euo pipefail
source "$ROOT_DIR/integration/bpf_mock/common/lib.sh"
if [[ $TEST_MODE == shared || $TEST_MODE == build_failure ]]; then
 export BPF_MOCK_BUILD_DIR="$HUATUO_BAMAI_TEST_TMPDIR/cache"
fi
go() {
 [[ $1 == build && $2 == -tags && $3 == integration && $4 == -o ]]
 echo build >> "$TEST_LOG"
 [[ $TEST_MODE != build_failure ]] || return 42
 cat > "$5" <<'CHECKER'
#!/bin/sh
[ "$1" = --fixture ]
CHECKER
 chmod +x "$5"
}
run_case() (
 export HUATUO_BAMAI_TEST_TMPDIR="$HUATUO_BAMAI_TEST_TMPDIR/$1"
 mkdir -p "$HUATUO_BAMAI_TEST_TMPDIR"
 # An unrelated executable in a reused workspace is not a validated build.
 printf '#!/bin/sh\nexit 99\n' > "$HUATUO_BAMAI_TEST_TMPDIR/memory_oom_kill-mock-checker"
 chmod +x "$HUATUO_BAMAI_TEST_TMPDIR/memory_oom_kill-mock-checker"
 run_bpf_mock_checker memory_oom_kill --fixture
 run_bpf_mock_checker memory_oom_kill --fixture
 if [[ $TEST_MODE == removed && $1 == first ]]; then
  rm "$HUATUO_BAMAI_TEST_TMPDIR/memory_oom_kill-mock-checker"
  run_bpf_mock_checker memory_oom_kill --fixture
 fi
)
run_case first
run_case second
`)
			cmd.Env = append(os.Environ(), "ROOT_DIR="+root, "HUATUO_BAMAI_TEST_TMPDIR="+workspace,
				"BPF_MOCK_BUILD_DIR=", "BPF_MOCK_CHECKER_BIN=", "TEST_MODE="+tc.name, "TEST_LOG="+filepath.Join(workspace, "builds"))
			output, err := cmd.CombinedOutput()
			if tc.wantExit == 0 {
				if err != nil {
					t.Fatalf("checker reuse: %v: %s", err, output)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != tc.wantExit {
					t.Fatalf("want exit %d: %v: %s", tc.wantExit, err, output)
				}
				if _, err := os.Stat(filepath.Join(workspace, "cache", "memory_oom_kill-mock-checker")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed checker build was cached: %v", err)
				}
			}
			builds, err := os.ReadFile(filepath.Join(workspace, "builds"))
			if err != nil || strings.Count(string(builds), "build\n") != tc.wantBuilds {
				t.Fatalf("checker builds: %q, want %d: %v", builds, tc.wantBuilds, err)
			}
		})
	}
}

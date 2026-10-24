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
	"strconv"
	"strings"
	"testing"
)

func TestBuildMockModules(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		mode     string
		wantExit int
	}{
		{mode: "success"},
		{mode: "missing headers", wantExit: 77},
		{mode: "build failure", wantExit: 1},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			workspace := t.TempDir()
			cmd := exec.Command("bash", "-c", `
set -euo pipefail
source "$ROOT_DIR/integration/bpf_mock/common/lib.sh"
source "$ROOT_DIR/integration/bpf_mock/common/lib.sh"
[[ $TEST_MODE == 'missing headers' ]] || mkdir -p "$KERNEL_BUILD_DIR"
make() {
 [[ $# == 3 && $1 == -C && $3 == "KERNEL_BUILD_DIR=$KERNEL_BUILD_DIR" ]] || return 1
 local feature=${2##*/}
 cmp "$ROOT_DIR/integration/bpf_mock/common/Makefile" "$2/Makefile" || return 1
 cmp "$ROOT_DIR/integration/bpf_mock/$feature/Kbuild" "$2/Kbuild" || return 1
 [[ -r $2/${feature}_mock_kmod.c && -r $2/${feature}_mock.h ]] || return 1
 [[ $feature != system_hungtask || -r $2/system_hungtask_trace.h ]] || return 1
 [[ ! -e $2/${feature}_trigger.c ]] || return 1
 printf 'build %s\n' "$feature"
 [[ $TEST_MODE != 'build failure' ]]
}
for feature in memory_oom_kill system_softlockup system_hungtask memory_reclaim_events memory_free_compact native_physical_alloc native_physical_usage native_virtual_alloc; do
 extra=()
 if [[ $feature == system_hungtask ]]; then
  extra+=("$ROOT_DIR/integration/bpf_mock/system_hungtask/system_hungtask_trace.h")
 fi
 if [[ $feature == memory_free_compact ]]; then
  extra+=("$ROOT_DIR/integration/bpf_mock/memory_free_compact/memory_free_compact_trace.h" "$ROOT_DIR/integration/bpf_mock/memory_free_compact/memory_free_compact_event.awk")
 fi
 if [[ $feature == memory_free_compact || $feature == native_* ]]; then
  extra+=("$ROOT_DIR/integration/bpf_mock/common/kernel_hook.awk")
 fi
 build_bpf_mock_module "$feature" "$HUATUO_BAMAI_TEST_TMPDIR/$feature" "${extra[@]}"
done
`)
			cmd.Env = append(os.Environ(), "ROOT_DIR="+root, "HUATUO_BAMAI_TEST_TMPDIR="+workspace,
				"KERNEL_BUILD_DIR="+filepath.Join(workspace, "kernel headers"), "TEST_MODE="+tc.mode)
			output, err := cmd.CombinedOutput()
			if tc.wantExit != 0 {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != tc.wantExit {
					t.Fatalf("want exit %d: %v: %s", tc.wantExit, err, output)
				}
			} else if err != nil {
				t.Fatalf("build fixtures: %v: %s", err, output)
			}
			if tc.wantExit != 77 {
				log, err := os.ReadFile(filepath.Join(workspace, "memory_oom_kill-mock-build.log"))
				if err != nil || !strings.Contains(string(log), "build memory_oom_kill") {
					t.Fatalf("missing module build log: %v: %s", err, log)
				}
			}
		})
	}
}

func TestMockCleanupPreservesStatus(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	const services = "daemon\n"
	const unload = "unload memory_oom_kill_mock\n"
	for _, tc := range []struct {
		name     string
		status   int
		failStep string
		unloaded bool
		wantExit int
		wantLog  string
	}{
		{name: "success", wantLog: services + unload},
		{name: "test failure", status: 42, wantExit: 42, wantLog: services + unload},
		{name: "skip", status: 77, wantExit: 77, wantLog: services + unload},
		{name: "daemon failure", failStep: "daemon", wantExit: 1, wantLog: services + unload},
		{name: "unload failure", failStep: "unload", wantExit: 1, wantLog: services + unload},
		{name: "module not owned", unloaded: true, wantLog: services},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			module := "memory_oom_kill_mock"
			if tc.unloaded {
				module = ""
			}
			cmd := exec.Command("bash", "-c", `
set -euo pipefail
source "$ROOT_DIR/integration/bpf_mock/common/core.sh"
huatuo_bamai_stop() { echo daemon >> "$TEST_LOG"; [[ $FAIL_STEP != daemon ]]; }
rmmod() { echo "unload $1" >> "$TEST_LOG"; [[ $FAIL_STEP != unload ]]; }
trap 'cleanup_bpf_mock "$?" "$LOADED_MODULE" || exit $?' EXIT
exit "$TEST_STATUS"
`)
			logPath := filepath.Join(workspace, "cleanup.log")
			cmd.Env = append(os.Environ(), "ROOT_DIR="+root, "TEST_LOG="+logPath,
				"TEST_STATUS="+strconv.Itoa(tc.status), "FAIL_STEP="+tc.failStep, "LOADED_MODULE="+module)
			output, err := cmd.CombinedOutput()
			if tc.wantExit == 0 {
				if err != nil {
					t.Fatalf("cleanup: %v: %s", err, output)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != tc.wantExit {
					t.Fatalf("want exit %d: %v: %s", tc.wantExit, err, output)
				}
			}
			log, err := os.ReadFile(logPath)
			if err != nil || string(log) != tc.wantLog {
				t.Fatalf("cleanup order: %v: got %q, want %q", err, log, tc.wantLog)
			}
		})
	}
}

func TestModuleBuildReuse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	cmd := exec.Command("bash", "-c", `
set -euo pipefail
source "$ROOT_DIR/integration/bpf_mock/common/lib.sh"
mkdir -p "$KERNEL_BUILD_DIR"
make() {
 echo build >> "$TEST_LOG"
 [[ $2 != */failure ]] || return 42
 printf 'fixture module\n' > "$2/memory_oom_kill_mock.ko"
}
# Failed builds must not populate the cache or prevent a later retry.
if (build_bpf_mock_module memory_oom_kill "$HUATUO_BAMAI_TEST_TMPDIR/failure"); then exit 1; fi
[[ ! -e $BPF_MOCK_BUILD_DIR/memory_oom_kill_mock.ko ]]
for name in host container; do
 build_bpf_mock_module memory_oom_kill "$HUATUO_BAMAI_TEST_TMPDIR/$name"
 cmp "$HUATUO_BAMAI_TEST_TMPDIR/$name/memory_oom_kill_mock.ko" "$BPF_MOCK_BUILD_DIR/memory_oom_kill_mock.ko"
done
# A fresh suite must rebuild, even though a previous suite's cache exists.
BPF_MOCK_BUILD_DIR="$HUATUO_BAMAI_TEST_TMPDIR/next-suite"
build_bpf_mock_module memory_oom_kill "$HUATUO_BAMAI_TEST_TMPDIR/next"
`)
	cmd.Env = append(os.Environ(), "ROOT_DIR="+root, "HUATUO_BAMAI_TEST_TMPDIR="+workspace,
		"KERNEL_BUILD_DIR="+filepath.Join(workspace, "headers"), "BPF_MOCK_BUILD_DIR="+filepath.Join(workspace, "cache"),
		"TEST_LOG="+filepath.Join(workspace, "builds"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("module reuse: %v: %s", err, output)
	}
	builds, err := os.ReadFile(filepath.Join(workspace, "builds"))
	if err != nil || string(builds) != "build\nbuild\nbuild\n" {
		t.Fatalf("failed, successful and fresh-suite builds: %q: %v", builds, err)
	}
}

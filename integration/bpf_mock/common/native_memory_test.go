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
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeMemoryLifecycle(t *testing.T) {
	realRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		feature, scope, scenarios string
		fail                      bool
		checks                    int
	}{
		{"native_physical_alloc", "host", "mixed", false, 3},
		{"native_physical_alloc", "container", "mixed", false, 2},
		{"native_physical_usage", "host", "retained balanced partial untracked", false, 6},
		{"native_physical_usage", "container", "retained balanced partial untracked", false, 5},
		{"native_virtual_alloc", "host", "mixed file-only", false, 4},
		{"native_virtual_alloc", "container", "mixed file-only", false, 3},
		{"native_physical_alloc", "host", "mixed", true, 0},
		{"native_physical_alloc", "container", "mixed", true, 0},
	} {
		name := tc.feature + "/" + tc.scope
		if tc.fail {
			name += "/profiler-failure"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			work := filepath.Join(root, "work")
			if err := os.MkdirAll(work, 0o700); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(root, "_output/bin")
			if err := os.MkdirAll(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			bpfDir := filepath.Join(root, "_output/bpf")
			if err := os.MkdirAll(bpfDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bpfDir, tc.feature+".o"), nil, 0o600); err != nil {
				t.Fatal(err)
			}
			profiler := `#!/usr/bin/env bash
set -euo pipefail
[[ $PWD == "$ROOT_DIR/_output" ]]
[[ " $* " == *' --bpf-mock '* ]]
if [[ $FEATURE == native_virtual_alloc ]]; then
 [[ " $* " != *' --physical-memory-probability '* ]]
else
 [[ " $* " == *' --physical-memory-probability 100 '* ]]
fi
if [[ $SCOPE == container ]]; then
 [[ " $* " == *' --container-id fixture-container '* ]]
 [[ " $* " != *' --pid '* ]]
fi
out=""; group=0
while (($#)); do
 case $1 in --output-path) out=$2;shift;; --thread-group) group=1;; esac
 shift
done
[[ -n $out ]]
if [[ $SCOPE != container ]]; then
 if [[ ${out##*/} == worker ]];then [[ $group == 1 ]];else [[ $group == 0 ]];fi
fi
printf 'symbol="target_mock"\ndata reading loop started\n'
for ((i=0;i<100;i++));do
 if jq -se 'any(.[]; .triggered == true)' "$out/trigger.jsonl" >/dev/null;then
  [[ $FAIL_PROFILER != 1 ]] || exit 23
  if [[ $SCOPE == container ]]; then
   jq -se 'any(.[]; .triggered == true)' "$out/excluded.jsonl" >/dev/null || { sleep 0.02; continue; }
  fi
  exit 0
 fi
 sleep 0.02
done
exit 24
`
			trigger := `#!/usr/bin/env bash
set -euo pipefail
comm=trigger;tid=$$
if [[ $2 == thread ]];then comm=mock-worker;tid=$((tid+1));fi
trap 'printf "{\"triggered\":true}\n"' USR1
trap 'exit 0' USR2
printf '{"pid":%d,"tid":%d,"comm":"%s"}\n' "$$" "$tid" "$comm"
while :;do sleep 0.02;done
`
			for path, contents := range map[string]string{filepath.Join(bin, "profiler"): profiler, filepath.Join(root, "fake-trigger"): trigger} {
				if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-c", `
set -euo pipefail
ROOT_DIR=$REAL_ROOT
source "$ROOT_DIR/integration/bpf_mock/common/native_memory.sh"
ROOT_DIR=$FAKE_ROOT
export ROOT_DIR
skip_known_mock_kprobe_kernel(){ :; }
require_commands(){ :; }
require_readable(){ :; }
KUBELET_CERT=fixture; KUBELET_KEY=fixture
allocate_available_port(){ echo 19200; }
integration_huatuo_bamai_start(){ :; }
create_bpf_mock_namespace(){ bpf_mock_namespace=$1; bpf_mock_pod=$2; }
create_bpf_mock_pod(){ :; }
describe_bpf_mock_pod(){ :; }
read_bpf_mock_container(){ printf '{"data":{"id":"fixture-container"}}' > "$4"; }
verify_bpf_mock_container_alive(){ [[ $3 == fixture-container ]]; }
stop_bpf_mock_daemon(){ echo drained >> "$TEST_LOG"; }
delete_bpf_mock_namespace(){ echo namespace-deleted >> "$TEST_LOG"; }
kubectl(){
 [[ " $* " == *' /host-proc '* ]]
 while [[ $1 != -- ]]; do shift; done
 shift 2
 exec bash "$FAKE_ROOT/fake-trigger" "$@"
}
kernel_version_le(){ return 1; }
build_bpf_mock_module(){ mkdir -p "$2"; }
compile_user_fixture(){ cp "$FAKE_ROOT/fake-trigger" "$2";chmod +x "$2"; }
insmod(){ echo load >> "$TEST_LOG"; }
rmmod(){ echo unload >> "$TEST_LOG"; }
test(){ if [[ ${1:-} == -e && ${2:-} == /dev/*_mock ]];then return 0;fi;builtin test "$@"; }
huatuo_bamai_stop(){ echo daemon >> "$TEST_LOG"; }
run_bpf_mock_checker(){
 [[ $1 == "$FEATURE" && $5 -gt 0 ]]
 [[ $(jq -r .pid "$3") -gt 0 ]]
 printf 'check %s\n' "$4" >> "$TEST_LOG"
}
run_native_memory_mock "$FEATURE" "$SCOPE" $SCENARIOS
`)
			fail := "0"
			if tc.fail {
				fail = "1"
			}
			log := filepath.Join(root, "lifecycle.log")
			cmd.Env = append(os.Environ(), "REAL_ROOT="+realRoot, "FAKE_ROOT="+root, "HUATUO_BAMAI_TEST_TMPDIR="+work, "FEATURE="+tc.feature, "SCOPE="+tc.scope, "SCENARIOS="+tc.scenarios, "TEST_LOG="+log, "FAIL_PROFILER="+fail)
			output, err := cmd.CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("lifecycle: %v: %s", err, output)
			}
			actual, readErr := os.ReadFile(log)
			if readErr != nil {
				t.Fatal(readErr)
			}
			cleanup := "daemon\nunload\n"
			if tc.scope == "container" {
				cleanup = "daemon\nnamespace-deleted\nunload\n"
			}
			if strings.Count(string(actual), "check ") != tc.checks || !strings.HasSuffix(string(actual), cleanup) {
				t.Fatalf("unexpected lifecycle: %s\n%s", actual, output)
			}
		})
	}
}

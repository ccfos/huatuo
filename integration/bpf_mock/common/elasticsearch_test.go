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

func TestMockElasticsearchLifecycle(t *testing.T) {
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq is required for the Elasticsearch helper")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		wantExit  int
		wantStart int
		wantIndex int
	}{
		{name: "shared", wantStart: 1, wantIndex: 2},
		{name: "private", wantStart: 2, wantIndex: 2},
		{name: "shared_failure", wantExit: 1, wantStart: 1, wantIndex: 2},
		{name: "shared_index_failure", wantExit: 1, wantStart: 1},
		{name: "shared_state_failure", wantExit: 1, wantStart: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			cmd := exec.Command("bash", "-c", `
set -euo pipefail
source "$ROOT_DIR/integration/lib.sh"
if [[ $TEST_MODE != private ]]; then
 export BPF_MOCK_ES_DIR="$HUATUO_BAMAI_TEST_TMPDIR/shared-es"
fi
stub_storage() {
 printf() {
  if [[ $TEST_MODE == shared_state_failure && $1 == STORAGE_CONTAINER_ID=* ]]; then
   builtin printf "STORAGE_CONTAINER_ID='"
   return 1
  fi
  builtin printf "$@"
 }
 require_commands() { :; }
 docker() { [[ $1 != logs ]] || printf 'shared ES diagnostics\n'; }
 storage_start() {
  [[ $1 == elasticsearch_v8 ]]
  printf 'start\n' >> "$TEST_LOG"
  STORAGE_CONTAINER_ID=mock-container
  STORAGE_ADDR=https://127.0.0.1:9200
  STORAGE_USERNAME=elastic
  STORAGE_PASSWORD=123456
 }
 storage_stop() {
  [[ -n $STORAGE_CONTAINER_ID ]] || return 0
  printf 'stop\n' >> "$TEST_LOG"
  STORAGE_CONTAINER_ID=""
 }
 storage_curl() {
  [[ $TEST_MODE != shared_index_failure ]] || return 1
  printf 'index %s\n' "$STORAGE_INDEX" >> "$TEST_LOG"
  printf '{"acknowledged":true}'
 }
}
finish_shared() {
 local status=$?
 [[ -n $BPF_MOCK_ES_DIR ]] || return 0
 source "$ROOT_DIR/integration/bpf_mock/common/elasticsearch.sh"
 stub_storage
 stop_shared_bpf_mock_es "$status"
}
trap finish_shared EXIT
run_case() (
 export HUATUO_BAMAI_TEST_TMPDIR="$HUATUO_BAMAI_TEST_TMPDIR/$1"
 mkdir -p "$HUATUO_BAMAI_TEST_TMPDIR"
 source "$ROOT_DIR/integration/config.sh"
 source "$ROOT_DIR/integration/bpf_mock/common/elasticsearch.sh"
 stub_storage
 trap stop_bpf_mock_es EXIT
 start_bpf_mock_es
 write_bpf_mock_es_config memory_oom_kill
)
run_case case-one.Abc
run_case case-two.Xyz
[[ $TEST_MODE != shared_failure ]] || exit 1
`)
			cmd.Env = append(os.Environ(), "ROOT_DIR="+root,
				"HUATUO_BAMAI_TEST_TMPDIR="+workspace, "BPF_MOCK_ES_DIR=",
				"TEST_LOG="+filepath.Join(workspace, "lifecycle.log"), "TEST_MODE="+tc.name)
			output, err := cmd.CombinedOutput()
			if tc.wantExit == 0 {
				if err != nil {
					t.Fatalf("helper lifecycle: %v: %s", err, output)
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != tc.wantExit {
					t.Fatalf("want exit %d: %v: %s", tc.wantExit, err, output)
				}
			}
			log, err := os.ReadFile(filepath.Join(workspace, "lifecycle.log"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(log), "start\n") != tc.wantStart ||
				strings.Count(string(log), "stop\n") != tc.wantStart ||
				strings.Count(string(log), "index ") != tc.wantIndex {
				t.Fatalf("unexpected lifecycle: %s", log)
			}
			if tc.name == "shared_state_failure" {
				if _, err := os.Stat(filepath.Join(workspace, "shared-es", "es.env")); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("failed state write transferred container ownership: %v", err)
				}
			} else if tc.wantExit != 0 {
				diagnostics, err := os.ReadFile(filepath.Join(workspace, "shared-es", "elasticsearch.log"))
				if err != nil || !strings.Contains(string(diagnostics), "shared ES diagnostics") {
					t.Fatalf("missing failure diagnostics: %v: %s", err, diagnostics)
				}
			} else if _, err := os.Stat(filepath.Join(workspace, "shared-es")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("shared workspace remains after success: %v", err)
			}
			if tc.wantIndex == 2 {
				for _, name := range []string{"case-one.Abc", "case-two.Xyz"} {
					cfg, err := os.ReadFile(filepath.Join(workspace, name, "bamai.conf"))
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(string(cfg), `Index = "huatuo_bpf_mock_`+strings.ToLower(name)+`"`) ||
						!strings.Contains(string(cfg), `Username = "elastic"`) ||
						!strings.Contains(string(cfg), `Password = "123456"`) {
						t.Fatalf("incorrect isolated ES config: %s", cfg)
					}
				}
			}
		})
	}
}

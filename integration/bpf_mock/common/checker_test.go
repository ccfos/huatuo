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
source "$ROOT_DIR/integration/lib.sh"
source "$ROOT_DIR/integration/lib_bpf_mock.sh"
command() {
 if [[ $* == '-v go' ]]; then return 1; fi
 builtin command "$@"
}
run_bpf_mock_checker oom --fixture
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

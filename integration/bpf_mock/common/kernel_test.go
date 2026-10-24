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

func TestKnownMockKprobeKernel(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, release := range []string{"5.4.0-216-generic", "5.4.0-215-generic", "5.4.0-217-generic", "5.4.0-custom"} {
		t.Run(release, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", `
set -euo pipefail
uname() { printf '%s\n' "$MOCK_KERNEL_RELEASE"; }
source "$ROOT_DIR/integration/bpf_mock/common/lib.sh"
skip_known_mock_kprobe_kernel
`)
			cmd.Env = append(os.Environ(), "ROOT_DIR="+root, "MOCK_KERNEL_RELEASE="+release)
			output, err := cmd.CombinedOutput()
			if release == "5.4.0-216-generic" {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 77 {
					t.Fatalf("expected SKIP (77): %v: %s", err, output)
				}
			} else if err != nil {
				t.Fatalf("unaffected kernel rejected: %v: %s", err, output)
			}
		})
	}
}

func TestMockKprobeScriptsSkipKnownKernel(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, script := range []string{"test_events_memory_oom_kill_mock.sh", "test_events_memory_oom_kill_mock_container.sh", "test_events_system_softlockup_mock.sh", "test_events_memory_reclaim_events_mock_container.sh", "test_metrics_memory_free_compact_mock.sh", "test_profiler_native_physical_alloc_mock.sh", "test_profiler_native_physical_usage_mock.sh", "test_profiler_native_virtual_alloc_mock.sh", "test_profiler_native_physical_alloc_mock_container.sh", "test_profiler_native_physical_usage_mock_container.sh", "test_profiler_native_virtual_alloc_mock_container.sh"} {
		t.Run(script, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", `
set -euo pipefail
uname() {
 if [[ $* == '-r' ]]; then printf '%s\n' 5.4.0-216-generic;
 else command uname "$@"; fi
}
source "$ROOT_DIR/integration/$1"
`, "test", script)
			cmd.Env = append(os.Environ(), "ROOT_DIR="+root)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 77 {
				t.Fatalf("expected early SKIP (77): %v: %s", err, output)
			}
			if !strings.Contains(string(output), "mock module kprobe attachment is unavailable") {
				t.Fatalf("unexpected skip reason: %s", output)
			}
		})
	}
}

func TestMockPlatform(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		release, arch, skipReason string
	}{
		{"4.17.0", "x86_64", "Linux 4.18 or newer"},
		{"4.18.0", "x86_64", ""},
		{"5.10.0", "aarch64", ""},
		{"5.10.0", "riscv64", "x86_64 or aarch64"},
	} {
		t.Run(tc.release+"/"+tc.arch, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", `
set -euo pipefail
uname() { if [[ $1 == -r ]]; then echo "$TEST_RELEASE"; else echo "$TEST_ARCH"; fi; }
source "$ROOT_DIR/integration/bpf_mock/common/lib.sh"
require_bpf_mock_platform
`)
			cmd.Env = append(os.Environ(), "ROOT_DIR="+root, "TEST_RELEASE="+tc.release, "TEST_ARCH="+tc.arch)
			output, err := cmd.CombinedOutput()
			if tc.skipReason != "" {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 77 || !strings.Contains(string(output), tc.skipReason) {
					t.Fatalf("expected platform skip: %v: %s", err, output)
				}
			} else if err != nil {
				t.Fatalf("supported platform rejected: %v: %s", err, output)
			}
		})
	}
}

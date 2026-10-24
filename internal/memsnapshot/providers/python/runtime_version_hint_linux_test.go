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

package python

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestDiscoverRuntimeVersionedExecutable exercises the reported failure end to
// end against a live interpreter: a CPython build that does not export
// Py_Version must still be discovered through its mapped executable name.
//
// The test is skipped unless a versioned interpreter is available, so it never
// fails on hosts that only ship a Py_Version-enabled build. Set
// AUDIT_PYTHON=/path/to/python3.10 to pin the interpreter.
func TestDiscoverRuntimeVersionedExecutable(t *testing.T) {
	executable := os.Getenv("AUDIT_PYTHON")
	if executable == "" {
		for _, candidate := range []string{
			"/usr/bin/python3.10", "/usr/bin/python3.9", "/usr/bin/python3.8",
			"/usr/bin/python3.11", "/usr/bin/python3.12", "/usr/bin/python3.13",
		} {
			if _, err := os.Stat(candidate); err == nil {
				executable = candidate
				break
			}
		}
	}
	if executable == "" {
		t.Skip("no versioned CPython interpreter available; set AUDIT_PYTHON to run")
	}

	cmd := exec.CommandContext(t.Context(), executable, "-c",
		"import sys; print('ready', flush=True); sys.stdin.readline()")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		_ = cmd.Wait()
	}()
	if _, err := bufio.NewReader(stdout).ReadString('\n'); err != nil {
		t.Fatal(err)
	}

	wantMinor := strings.TrimPrefix(executable, "/usr/bin/python3.")
	if wantMinor == executable {
		t.Skipf("cannot derive the minor version from %q", executable)
	}

	target, err := discoverRuntime(t.Context(), "/proc", cmd.Process.Pid,
		newMemory(cmd.Process.Pid, t.Context()))
	if err != nil {
		t.Fatalf("discoverRuntime() for %s: %v", executable, err)
	}
	if target.version.major != 3 || target.version.minor == 0 {
		t.Fatalf("version = %s, want a 3.x interpreter", target.version)
	}
	// Py_Version is authoritative when the build exports it; otherwise the
	// mapped executable name must supply the same version.
	if got := target.version.minor; got != atoiOrZero(wantMinor) {
		t.Fatalf("version = %s, want 3.%s for %s", target.version, wantMinor, executable)
	}
}

func atoiOrZero(text string) int {
	value := 0
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0
		}
		value = value*10 + int(r-'0')
	}
	return value
}

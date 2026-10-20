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

package provider

import (
	"os/exec"
	"strings"
	"testing"
)

func TestPythonProcessNameWithoutScript(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for live process metadata coverage")
	}
	for _, args := range [][]string{nil, {"-"}, {"-u"}, {"-i"}} {
		t.Run(strings.Join(append([]string{"python3"}, args...), " "), func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), python, args...)
			// Keep stdin open: CPython waits for a program or interactive input.
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				_ = stdin.Close()
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_ = stdin.Close()
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			})
			entry, err := pythonProcessName(cmd.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			if entry != "python3" {
				t.Fatalf("entry = %q, want python3", entry)
			}
		})
	}
}

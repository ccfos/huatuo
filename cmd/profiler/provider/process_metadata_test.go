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
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReadProcessLabels(t *testing.T) {
	for _, pid := range []int{-1, 0} {
		if _, err := readProcessLabels(pid); err == nil {
			t.Fatal("invalid process accepted")
		}
	}
}

func TestPythonProcessNameExecutableNames(t *testing.T) {
	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required for the live process test")
	}
	pythonBinary, err := os.ReadFile(pythonPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"python3", "platform-python3.6", "unrelated"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			executable := filepath.Join(dir, name)
			if err := os.WriteFile(executable, pythonBinary, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(executable, 0o700); err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(dir, "main.py")
			if err := os.WriteFile(script, []byte("import time\nprint('ready', flush=True)\ntime.sleep(30)\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, executable, "-u", script)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				cancel()
				_ = cmd.Wait()
			}()
			if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
				t.Fatalf("Python process did not become ready: line=%q, err=%v", line, err)
			}
			entry, err := pythonProcessName(cmd.Process.Pid)
			if name == "unrelated" {
				if err == nil {
					t.Fatal("unrelated executable accepted as Python")
				}
				return
			}
			if err != nil || entry != script {
				t.Fatalf("entry=%q, err=%v; want %q", entry, err, script)
			}
		})
	}
}

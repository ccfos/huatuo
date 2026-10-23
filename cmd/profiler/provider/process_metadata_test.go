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
	"runtime"
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

func TestJavaProcessName(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("procfs is required for the live process test")
	}

	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "add-modules",
			args: []string{"--add-modules", "java.sql", "-cp", ".", "Main"},
			want: "Main",
		},
		{
			name: "class-path",
			args: []string{"--class-path", ".", "-jar", "app.jar"},
			want: "app.jar",
		},
		{
			name: "main-class",
			args: []string{"-Xmx256m", "com.example.Main", "arg"},
			want: "com.example.Main",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executable := filepath.Join(t.TempDir(), "java")
			if err := os.Symlink(self, executable); err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			args := append([]string{"-test.run=TestJavaProcessNameHelper", "--"}, tt.args...)
			cmd := exec.CommandContext(ctx, executable, args...)
			cmd.Env = append(os.Environ(), "HUATUO_TEST_JAVA_PROCESS=1")
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				cancel()
				_ = cmd.Wait()
			}()

			entry, err := javaProcessName(cmd.Process.Pid)
			if err != nil || entry != tt.want {
				t.Fatalf("entry=%q, err=%v; want %q", entry, err, tt.want)
			}
		})
	}
}

func TestJavaProcessNameHelper(t *testing.T) {
	if os.Getenv("HUATUO_TEST_JAVA_PROCESS") != "1" {
		return
	}

	time.Sleep(30 * time.Second)
}

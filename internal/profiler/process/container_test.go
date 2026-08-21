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

package process

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"huatuo-bamai/internal/procfs"
)

type testProcess struct {
	pid        int
	executable string
	ppid       int
	cmdline    []string
}

func TestExecutableFilterValidate(t *testing.T) {
	tests := []struct {
		name    string
		filter  ExecutableFilter
		wantErr bool
	}{
		{
			name:   "exact name",
			filter: ExecutableFilter{ExecutableName: "java"},
		},
		{
			name:   "name prefix",
			filter: ExecutableFilter{ExecutablePrefix: "python"},
		},
		{
			name:    "missing name",
			filter:  ExecutableFilter{},
			wantErr: true,
		},
		{
			name: "ambiguous name",
			filter: ExecutableFilter{
				ExecutableName:   "java",
				ExecutablePrefix: "java",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.filter.validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("ExecutableFilter.validate() error = %v, wantErr %t", err, tt.wantErr)
			}
		})
	}
}

func TestFindProcessesMatchesExactExecutableName(t *testing.T) {
	root, procFS := newTestProcFS(t)
	writeTestProcess(t, root, testProcess{pid: 100, executable: "/usr/bin/java", ppid: 1})
	writeTestProcess(t, root, testProcess{pid: 101, executable: "/usr/bin/javac", ppid: 1})

	got, err := findProcesses(
		[]int32{100, 101},
		procFS,
		ExecutableFilter{ExecutableName: "java"},
	)
	if err != nil {
		t.Fatalf("findProcesses() error = %v", err)
	}
	want := map[int][]int{100: {100}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findProcesses() = %v, want %v", got, want)
	}
}

func TestFindProcessesMatchesUnlinkedExecutable(t *testing.T) {
	root, procFS := newTestProcFS(t)
	writeTestProcess(t, root, testProcess{pid: 100, executable: "/usr/bin/java (deleted)", ppid: 1})

	got, err := findProcesses(
		[]int32{100},
		procFS,
		ExecutableFilter{
			ExecutableName: "java",
			ExecutablePath: "/usr/bin/java",
		},
	)
	if err != nil {
		t.Fatalf("findProcesses() error = %v", err)
	}
	want := map[int][]int{100: {100}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findProcesses() = %v, want %v", got, want)
	}
}

func TestFindProcessesMatchesExactCommandLineArgument(t *testing.T) {
	root, procFS := newTestProcFS(t)
	writeTestProcess(t, root, testProcess{
		pid:        100,
		executable: "/usr/bin/python3",
		ppid:       1,
		cmdline:    []string{"/usr/bin/python3", "/app/main.py"},
	})
	writeTestProcess(t, root, testProcess{
		pid:        101,
		executable: "/usr/bin/python3",
		ppid:       1,
		cmdline:    []string{"/usr/bin/python3", "/app/main.py.backup"},
	})

	got, err := findProcesses(
		[]int32{100, 101},
		procFS,
		ExecutableFilter{
			ExecutablePrefix: "python",
			ExecutablePath:   "/app/main.py",
		},
	)
	if err != nil {
		t.Fatalf("findProcesses() error = %v", err)
	}
	want := map[int][]int{100: {100}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("findProcesses() = %v, want %v", got, want)
	}
}

func TestFindProcessesSkipsExitedProcess(t *testing.T) {
	root, procFS := newTestProcFS(t)
	writeTestProcess(t, root, testProcess{pid: 100, executable: "/usr/bin/java", ppid: 1})
	statPath := filepath.Join(root, "100", "stat")
	if err := os.Remove(statPath); err != nil {
		t.Fatalf("Remove(%q) error = %v", statPath, err)
	}

	got, err := findProcesses(
		[]int32{100},
		procFS,
		ExecutableFilter{ExecutableName: "java"},
	)
	if err != nil {
		t.Fatalf("findProcesses() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("findProcesses() = %v, want empty result", got)
	}
}

func TestFindProcessesReturnsStatParseError(t *testing.T) {
	root, procFS := newTestProcFS(t)
	writeTestProcess(t, root, testProcess{pid: 100, executable: "/usr/bin/java", ppid: 1})
	statPath := filepath.Join(root, "100", "stat")
	if err := os.WriteFile(statPath, []byte("invalid stat"), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", statPath, err)
	}

	_, err := findProcesses(
		[]int32{100},
		procFS,
		ExecutableFilter{ExecutableName: "java"},
	)
	if err == nil {
		t.Fatal("findProcesses() error = nil, want stat parse error")
	}
}

func TestGroupProcessesByRootTraversesAllAncestors(t *testing.T) {
	processes := []procInfo{
		{pid: 100, ppid: 1},
		{pid: 101, ppid: 100},
		{pid: 102, ppid: 101},
		{pid: 200, ppid: 1},
	}
	got, err := groupProcessesByRoot(processes)
	if err != nil {
		t.Fatalf("groupProcessesByRoot() error = %v", err)
	}
	want := map[int][]int{
		100: {100, 101, 102},
		200: {200},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groupProcessesByRoot() = %v, want %v", got, want)
	}
}

func TestGroupProcessesByRootRejectsParentCycle(t *testing.T) {
	processes := []procInfo{
		{pid: 100, ppid: 101},
		{pid: 101, ppid: 100},
	}

	if _, err := groupProcessesByRoot(processes); err == nil {
		t.Fatal("groupProcessesByRoot() error = nil, want parent cycle error")
	}
}

func newTestProcFS(t *testing.T) (string, procfs.FS) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "proc")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", root, err)
	}

	procFS, err := procfs.NewFS(root)
	if err != nil {
		t.Fatalf("procfs.NewFS(%q) error = %v", root, err)
	}

	return root, procFS
}

func writeTestProcess(t *testing.T, root string, process testProcess) {
	t.Helper()
	processPath := filepath.Join(root, fmt.Sprintf("%d", process.pid))
	if err := os.MkdirAll(processPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", processPath, err)
	}
	if err := os.Symlink(process.executable, filepath.Join(processPath, "exe")); err != nil {
		t.Fatalf("Symlink(%q) error = %v", process.executable, err)
	}

	stat := fmt.Sprintf("%d (%s) S %d ", process.pid, filepath.Base(process.executable), process.ppid) +
		strings.Repeat("0 ", 40)
	statPath := filepath.Join(processPath, "stat")
	if err := os.WriteFile(statPath, []byte(stat), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", statPath, err)
	}

	if len(process.cmdline) == 0 {
		return
	}
	cmdline := strings.Join(process.cmdline, "\x00") + "\x00"
	cmdlinePath := filepath.Join(processPath, "cmdline")
	if err := os.WriteFile(cmdlinePath, []byte(cmdline), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", cmdlinePath, err)
	}
}

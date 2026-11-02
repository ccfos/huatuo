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
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ccfos/huatuo/internal/procfs"
)

const deletedExecutableMarker = " (deleted)"

func TestValidateMaxProfilerProcesses(t *testing.T) {
	tests := []struct {
		name         string
		profilerName string
		pids         []int
		maximum      int
		wantError    string
	}{
		{name: "unlimited", profilerName: "Java", pids: []int{1, 2}},
		{
			name:         "negative maximum",
			profilerName: "Java",
			pids:         []int{1, 2},
			maximum:      -1,
			wantError:    "start Java profiler: maximum profiler processes must not be negative",
		},
		{name: "within maximum", profilerName: "Python", pids: []int{1, 2}, maximum: 2},
		{
			name:         "over limit",
			profilerName: "Python",
			pids:         []int{1, 2},
			maximum:      1,
			wantError:    "start Python profiler: too many profiler processes: maximum=1, required=2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMaxProfilerProcesses(tt.profilerName, tt.pids, tt.maximum)
			if tt.wantError != "" {
				require.EqualError(t, err, tt.wantError)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateResolvedPIDs(t *testing.T) {
	require.NoError(t, validateResolvedPIDs("Java", []int{1}))
	require.EqualError(t, validateResolvedPIDs("Java", nil), "start Java profiler: no target processes found")
}

func TestHasExecutablePrefix(t *testing.T) {
	tests := map[string]bool{
		"python3.12":         true,
		"python3 custom":     true,
		"platform-python3.6": true,
		"java":               false,
		"my-python":          false,
	}
	for name, want := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, want, hasExecutablePrefix(name, "python"))
		})
	}
	require.False(t, hasExecutablePrefix("platform-java", "java"))
	require.True(t, hasExecutablePrefix("python3 custom", "python"))
}

// newExecTestProcFS redirects the procfs mount point to a temporary directory, so
// executable lookups do not depend on the host process table.
func newExecTestProcFS(t *testing.T) string {
	t.Helper()

	tmpRoot := t.TempDir()
	originalPrefix := filepath.Dir(procfs.DefaultPath())
	procfs.RootPrefix(tmpRoot)
	t.Cleanup(func() { procfs.RootPrefix(originalPrefix) })

	procPath := filepath.Join(tmpRoot, "proc")
	if err := os.MkdirAll(procPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", procPath, err)
	}

	return procPath
}

// writeTestExecutable creates the fake /proc/<pid>/exe link used by the
// executable checks.
func writeTestExecutable(t *testing.T, procPath string, pid int, target string) {
	t.Helper()

	processPath := filepath.Join(procPath, strconv.Itoa(pid))
	if err := os.MkdirAll(processPath, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", processPath, err)
	}
	if err := os.Symlink(target, filepath.Join(processPath, "exe")); err != nil {
		t.Fatalf("Symlink(%q) error = %v", target, err)
	}
}

func TestValidateProcessExecutablesAcceptsUnlinkedExecutable(t *testing.T) {
	tests := []struct {
		name   string
		prof   string
		prefix string
		target string
	}{
		{name: "python", prof: "Python", prefix: "python", target: "/huatuo-nonexistent/bin/python3.10"},
		{name: "unlinked python", prof: "Python", prefix: "python", target: "/huatuo-nonexistent/bin/python3.10 (deleted)"},
		{name: "unlinked platform python", prof: "Python", prefix: "python", target: "/huatuo-nonexistent/libexec/platform-python3.6 (deleted)"},
		{name: "unlinked java", prof: "Java", prefix: "java", target: "/huatuo-nonexistent/jvm/java-17-openjdk/bin/java (deleted)"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			procPath := newExecTestProcFS(t)
			writeTestExecutable(t, procPath, 100, test.target)

			require.NoError(t, validateProcessExecutables(test.prof, test.prefix, []int{100}))
		})
	}
}

func TestValidateProcessExecutablesRejectsUnexpectedExecutable(t *testing.T) {
	procPath := newExecTestProcFS(t)
	writeTestExecutable(t, procPath, 100, "/huatuo-nonexistent/bin/ruby3.1 (deleted)")

	require.EqualError(
		t,
		validateProcessExecutables("Python", "python", []int{100}),
		`Python PID 100 executable name "ruby3.1", want prefix "python"`,
	)
}

func TestValidateExpectedExecPath(t *testing.T) {
	procPath := newExecTestProcFS(t)
	base := filepath.Join(t.TempDir(), "python3.10")
	writeTestExecutable(t, procPath, 100, base+deletedExecutableMarker)

	// An in-place upgrade unlinks the running image, so the interpreter keeps
	// running while procfs reports the marker. The requested path still points
	// at that binary and must be accepted.
	require.NoError(t, validateExpectedExecPath([]int{100}, base))
	require.NoError(t, validateExpectedExecPath([]int{100}, ""))

	// The marker is only meaningful on the procfs side. A real file whose name
	// ends in " (deleted)" is a different path and must not match.
	require.EqualError(
		t,
		validateExpectedExecPath([]int{100}, base+deletedExecutableMarker),
		`PID 100 executable "`+base+`", want "`+base+deletedExecutableMarker+`"`,
	)
}

func TestValidateExpectedExecPathRejectsLiteralDeletedSuffix(t *testing.T) {
	procPath := newExecTestProcFS(t)
	literal := filepath.Join(t.TempDir(), "python3.10 (deleted)")
	if err := os.WriteFile(literal, nil, 0o700); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", literal, err)
	}
	writeTestExecutable(t, procPath, 100, literal)

	require.EqualError(
		t,
		validateExpectedExecPath([]int{100}, filepath.Join(filepath.Dir(literal), "python3.10")),
		`PID 100 executable "`+literal+`", want "`+filepath.Join(filepath.Dir(literal), "python3.10")+`"`,
	)
}

func TestValidateExpectedExecPathAcceptsReplacedExecutable(t *testing.T) {
	procPath := newExecTestProcFS(t)
	dir := t.TempDir()
	current := filepath.Join(dir, "python3.10")
	if err := os.WriteFile(current, nil, 0o700); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", current, err)
	}
	writeTestExecutable(t, procPath, 100, current+deletedExecutableMarker)

	// An in-place upgrade replaces the path while the old inode keeps running.
	// The marked procfs target still identifies the Python interpreter, and the
	// user-provided path is the original one.
	require.NoError(t, validateExpectedExecPath([]int{100}, current))
}

func TestValidateExpectedExecPathRejectsOtherExecutable(t *testing.T) {
	procPath := newExecTestProcFS(t)
	base := filepath.Join(t.TempDir(), "python3.10")
	writeTestExecutable(t, procPath, 100, base+"1"+deletedExecutableMarker)

	require.EqualError(
		t,
		validateExpectedExecPath([]int{100}, base),
		`PID 100 executable "`+base+`1", want "`+base+`"`,
	)
}

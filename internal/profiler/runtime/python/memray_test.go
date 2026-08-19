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
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPythonVersion(t *testing.T) {
	version := PythonVersion{Major: 3, Minor: 9}
	if got := version.String(); got != "3.9" {
		t.Fatalf("String() = %q, want %q", got, "3.9")
	}
	if got := version.RuntimeKey(); got != "py3.9" {
		t.Fatalf("RuntimeKey() = %q, want %q", got, "py3.9")
	}
}

func TestListMemrayRuntimePaths(t *testing.T) {
	bundle := t.TempDir()
	pythonPath := filepath.Join(bundle, "runtimes", "py3.9", "python")
	if err := os.MkdirAll(filepath.Join(pythonPath, "memray"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bundle, "runtimes", "incomplete"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := listMemrayRuntimePaths(bundle)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"py3.9": pythonPath}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listMemrayRuntimePaths() = %#v, want %#v", got, want)
	}
}

func TestResolveMemrayPythonPathLegacyLayout(t *testing.T) {
	bundle := t.TempDir()
	pythonPath := filepath.Join(bundle, "python")
	if err := os.MkdirAll(filepath.Join(pythonPath, "memray"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, _, _, err := ResolveMemrayPythonPath(os.Getpid(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if got != pythonPath {
		t.Fatalf("ResolveMemrayPythonPath() = %q, want %q", got, pythonPath)
	}
}

func TestSelectMemrayInjector(t *testing.T) {
	pythonPath := t.TempDir()
	memrayPath := filepath.Join(pythonPath, "memray")
	if err := os.Mkdir(memrayPath, 0o755); err != nil {
		t.Fatal(err)
	}

	const (
		abi3Injector = "_inject.abi3.so"
		py39Injector = "_inject.cpython-39-x86_64-linux-gnu.so"
	)
	for _, name := range []string{abi3Injector, py39Injector} {
		if err := os.WriteFile(filepath.Join(memrayPath, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := SelectMemrayInjector(
		pythonPath,
		PythonVersion{Major: 3, Minor: 9},
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != py39Injector {
		t.Fatalf("SelectMemrayInjector() = %q, want %q", got, py39Injector)
	}
}

func TestPythonVersionFromRuntimeNames(t *testing.T) {
	tests := []struct {
		name  string
		match []string
		want  PythonVersion
	}{
		{
			name:  "shared library",
			match: libpythonVersionRe.FindStringSubmatch("/usr/lib/libpython3.8.so.1.0"),
			want:  PythonVersion{Major: 3, Minor: 8},
		},
		{
			name:  "executable",
			match: pythonExecutableVersionRe.FindStringSubmatch("python3.9"),
			want:  PythonVersion{Major: 3, Minor: 9},
		},
		{
			name:  "debug executable",
			match: pythonExecutableVersionRe.FindStringSubmatch("python3.11d"),
			want:  PythonVersion{Major: 3, Minor: 11},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok, err := pythonVersionFromMatch(test.match)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				t.Fatal("version was not detected")
			}
			if got != test.want {
				t.Fatalf("version = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestMemrayRuntimeKey(t *testing.T) {
	if got := memrayRuntimeKey(
		"/bundle/runtimes/py3.8/python",
		PythonVersion{},
		false,
	); got != "py3.8" {
		t.Fatalf("versioned runtime key = %q, want py3.8", got)
	}
	if got := memrayRuntimeKey(
		"/bundle/python",
		PythonVersion{Major: 3, Minor: 9},
		true,
	); got != "py3.9" {
		t.Fatalf("detected runtime key = %q, want py3.9", got)
	}
	if got := memrayRuntimeKey("/bundle/python", PythonVersion{}, false); got != "legacy" {
		t.Fatalf("legacy runtime key = %q, want legacy", got)
	}
}

func TestMemrayRuntimeContentIDChangesWithContent(t *testing.T) {
	runtimePath := t.TempDir()
	filePath := filepath.Join(runtimePath, "runtime.so")
	if err := os.WriteFile(filePath, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	first, err := memrayRuntimeContentID(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := memrayRuntimeContentID(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("runtime content ID did not change")
	}
}

func TestCopyMemrayRuntime(t *testing.T) {
	source := t.TempDir()
	sourceFile := filepath.Join(source, "memray", "runtime.so")
	if err := os.MkdirAll(filepath.Dir(sourceFile), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourceFile, []byte("runtime"), 0o600); err != nil {
		t.Fatal(err)
	}

	destination := filepath.Join(t.TempDir(), "python")
	if err := copyMemrayRuntime(source, destination); err != nil {
		t.Fatal(err)
	}
	destinationFile := filepath.Join(destination, "memray", "runtime.so")
	data, err := os.ReadFile(destinationFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "runtime" {
		t.Fatalf("copied content = %q, want runtime", data)
	}
	info, err := os.Stat(destinationFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("copied mode = %o, want 600", got)
	}
}

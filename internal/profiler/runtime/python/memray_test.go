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
	"strings"
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

func TestMemrayRelativePathsUseProfilerWorkingDirectory(t *testing.T) {
	profilerDir, targetDir := t.TempDir(), t.TempDir()
	want := filepath.Join(profilerDir, "bundle", "python")
	if err := os.MkdirAll(filepath.Join(want, "memray"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(profilerDir)
	bundle, err := ResolveMemrayBundlePath("bundle")
	if err != nil || bundle != filepath.Dir(want) {
		t.Fatalf("ResolveMemrayBundlePath() = %q, %v, want %q", bundle, err, filepath.Dir(want))
	}
	pythonPath, _, _, err := ResolveMemrayPythonPath(os.Getpid(), "bundle")
	if err != nil || pythonPath != want {
		t.Fatalf("ResolveMemrayPythonPath() = %q, %v, want %q", pythonPath, err, want)
	}
	targetPath, err := EnsureMemrayPython(os.Getpid(), "bundle/python", "", "", PythonVersion{}, false)
	if err != nil || targetPath != want {
		t.Fatalf("EnsureMemrayPython() = %q, %v, want %q", targetPath, err, want)
	}
	t.Chdir(targetDir)
	if _, err := os.Stat(filepath.Join(targetPath, "memray")); err != nil {
		t.Fatalf("runtime path depends on the target working directory: %v", err)
	}
}

func TestMemrayRuntimeRejectsSourceLinks(t *testing.T) {
	for _, name := range []string{"relative file", "absolute file", "directory", "runtime root"} {
		t.Run(name, func(t *testing.T) {
			parent := t.TempDir()
			source := filepath.Join(parent, "python")
			if err := os.MkdirAll(filepath.Join(source, "memray"), 0o700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(source, "dependency.py")
			if err := os.WriteFile(file, []byte("# dependency"), 0o600); err != nil {
				t.Fatal(err)
			}
			link, target := filepath.Join(source, "link"), "dependency.py"
			switch name {
			case "absolute file":
				target = file
			case "directory":
				target = "memray"
			case "runtime root":
				link, target = filepath.Join(parent, "linked-python"), "python"
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if name == "runtime root" {
				source = link
			}
			if _, err := EnsureMemrayPython(os.Getpid(), source, "", "", PythonVersion{}, false); err == nil {
				t.Fatal("host runtime accepted a source link")
			}
			if _, err := memrayRuntimeContentID(source); err == nil {
				t.Fatal("runtime fingerprint accepted a source link")
			}
			destination, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer destination.Close()
			if err := copyMemrayRuntime(source, destination); err == nil {
				t.Fatal("runtime copy accepted a source link")
			}
		})
	}
}

func TestSelectMemrayInjector(t *testing.T) {
	const (
		abi3Injector = "_inject.abi3.so"
		py39Injector = "_inject.cpython-39-x86_64-linux-gnu.so"
		py37Injector = "_inject.cpython-37m-x86_64-linux-gnu.so"
	)
	for _, test := range []struct {
		name    string
		version PythonVersion
		known   bool
		files   []string
		want    string
	}{
		{"exact before abi3", PythonVersion{3, 9}, true, []string{abi3Injector, py39Injector}, py39Injector},
		{"legacy pymalloc tag", PythonVersion{3, 7}, true, []string{py37Injector}, py37Injector},
		{"stable ABI minimum", PythonVersion{3, 7}, true, []string{abi3Injector}, abi3Injector},
		{"stable ABI fallback", PythonVersion{3, 8}, true, []string{abi3Injector, py39Injector}, abi3Injector},
		{"wrong version", PythonVersion{3, 8}, true, []string{py39Injector}, ""},
		{"near version", PythonVersion{3, 9}, true, []string{"_inject.cpython-390-x86_64-linux-gnu.so"}, ""},
		{"free threaded tag", PythonVersion{3, 13}, true, []string{"_inject.cpython-313t-x86_64-linux-gnu.so"}, ""},
		{"unversioned", PythonVersion{3, 9}, true, []string{"_inject.so"}, ""},
		{"below stable ABI minimum", PythonVersion{3, 6}, true, []string{abi3Injector}, ""},
		{"different major", PythonVersion{4, 0}, true, []string{abi3Injector}, ""},
		{"unknown legacy runtime", PythonVersion{}, false, []string{abi3Injector, py39Injector}, py39Injector},
		{"unknown stable ABI runtime", PythonVersion{}, false, []string{abi3Injector}, abi3Injector},
		{"missing", PythonVersion{3, 9}, true, nil, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			pythonPath := t.TempDir()
			memrayPath := filepath.Join(pythonPath, "memray")
			if err := os.Mkdir(memrayPath, 0o755); err != nil {
				t.Fatal(err)
			}
			for _, name := range test.files {
				if err := os.WriteFile(filepath.Join(memrayPath, name), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := SelectMemrayInjector(pythonPath, test.version, test.known)
			if test.want == "" {
				if err == nil || got != "" {
					t.Fatalf("SelectMemrayInjector() = %q, %v, want error", got, err)
				}
				if len(test.files) > 0 && (!strings.Contains(err.Error(), test.version.String()) ||
					!strings.Contains(err.Error(), test.files[0])) {
					t.Fatalf("error must identify target version and available injector: %v", err)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("SelectMemrayInjector() = %q, %v, want %q", got, err, test.want)
			}
		})
	}
}

func TestStageMemrayRuntimeConfinesPaths(t *testing.T) {
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "memray"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"_inject.abi3.so": "memray_schedule_client_direct",
		"_memray.so":      "extension",
	} {
		if err := os.WriteFile(filepath.Join(source, "memray", name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	contentID, err := memrayRuntimeContentID(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		link      string
		target    string
		wantError bool
	}{
		{name: "ordinary"},
		{name: "relative within root", link: "tmp", target: "scratch"},
		{name: "absolute parent", link: "tmp", target: "absolute", wantError: true},
		{name: "relative escape", link: "tmp", target: "outside", wantError: true},
		{name: "absolute bundle", link: "tmp/memray", target: "absolute", wantError: true},
		{name: "absolute runtime", link: "tmp/memray/python", target: "absolute", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			rootPath, outside := t.TempDir(), t.TempDir()
			if err := os.Mkdir(filepath.Join(rootPath, "scratch"), 0o755); err != nil {
				t.Fatal(err)
			}
			if test.link != "" {
				link := filepath.Join(rootPath, test.link)
				if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
					t.Fatal(err)
				}
				target := test.target
				switch target {
				case "absolute":
					target = outside
				case "outside":
					var err error
					target, err = filepath.Rel(filepath.Dir(link), outside)
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := os.Symlink(target, link); err != nil {
					t.Fatal(err)
				}
			}
			root, err := os.OpenRoot(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			err = stageMemrayRuntime(source, root, "tmp/memray/python", "_inject.abi3.so", contentID)
			if (err != nil) != test.wantError {
				t.Fatalf("stageMemrayRuntime() = %v, wantError %v", err, test.wantError)
			}
			if !test.wantError {
				if !stagedMemrayRuntimeValid(root, "tmp/memray/python", "_inject.abi3.so", contentID) {
					t.Fatal("staged runtime is invalid")
				}
				if err := stageMemrayRuntime(source, root, "tmp/memray/python", "_inject.abi3.so", contentID); err != nil {
					t.Fatalf("reuse runtime: %v", err)
				}
			}
			entries, err := os.ReadDir(outside)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("staging wrote outside the target root: %v", entries)
			}
		})
	}
}

func TestStageMemrayRuntimeVerifiesCachedContents(t *testing.T) {
	source := t.TempDir()
	files := map[string]string{
		"memray/_inject.abi3.so":             "memray_schedule_client_direct",
		"memray/_memray.so":                  "extension",
		"dependency.py":                      "# dependency",
		"__pycache__/dependency.cpython.pyc": "packaged bytecode",
	}
	for path, content := range files {
		path = filepath.Join(source, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	contentID, err := memrayRuntimeContentID(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		path      string
		content   string
		remove    bool
		wantError bool
	}{
		{name: "unchanged"},
		{name: "truncated extension", path: "memray/_memray.so", wantError: true},
		{name: "replaced injector", path: "memray/_inject.abi3.so", content: "memray_schedule_client_direct changed", wantError: true},
		{name: "missing dependency", path: "dependency.py", remove: true, wantError: true},
		{name: "same size changed dependency", path: "dependency.py", content: "# DEPENDENCY", wantError: true},
		{name: "extra source file", path: "unexpected.py", content: "# unexpected", wantError: true},
		{name: "regenerated bytecode", path: "__pycache__/dependency.cpython.pyc", content: "regenerated"},
		{name: "new bytecode", path: "__pycache__/dependency.cpython.opt-1.pyc", content: "optimized"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rootPath := t.TempDir()
			root, err := os.OpenRoot(rootPath)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := stageMemrayRuntime(source, root, "tmp/memray/python", "_inject.abi3.so", contentID); err != nil {
				t.Fatal(err)
			}
			if test.path != "" {
				path := filepath.Join(rootPath, "tmp/memray/python", test.path)
				if test.remove {
					err = os.Remove(path)
				} else {
					err = os.WriteFile(path, []byte(test.content), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			err = stageMemrayRuntime(source, root, "tmp/memray/python", "_inject.abi3.so", contentID)
			if (err != nil) != test.wantError {
				t.Fatalf("cache reuse = %v, wantError %v", err, test.wantError)
			}
			if test.wantError && !strings.Contains(err.Error(), "differs from the source bundle") {
				t.Fatalf("unexpected cache validation error: %v", err)
			}
		})
	}
}

func TestCopyMemrayRuntimePinsDirectory(t *testing.T) {
	source, parent, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "runtime.so"), []byte("runtime"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(parent, "stage")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	destination, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer destination.Close()
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if err := copyMemrayRuntime(source, destination); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, "python", "runtime.so")); err != nil {
		t.Fatalf("copy did not use the opened directory: %v", err)
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("copy followed a replaced staging path")
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

	destinationRoot, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer destinationRoot.Close()
	if err := copyMemrayRuntime(source, destinationRoot); err != nil {
		t.Fatal(err)
	}
	destinationFile := filepath.Join(destinationRoot.Name(), "python", "memray", "runtime.so")
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

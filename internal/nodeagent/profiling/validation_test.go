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

package profiling

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	profilingdomain "github.com/ccfos/huatuo/pkg/profiling"
)

func TestValidateEnvironmentUsesSharedToolDir(t *testing.T) {
	config := testProfilingConfig(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config.ProfilerPath = executable
	config.ToolDir = t.TempDir()
	binary := filepath.Join(config.ToolDir, "py-spy")
	if err := os.WriteFile(binary, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(binary, 0o700); err != nil {
		t.Fatal(err)
	}
	request := testProfilingRequest()
	request.Spec.Language = profilingdomain.LanguagePython
	if err := validateEnvironment(request, config); err != nil {
		t.Fatalf("Python environment with only py-spy installed: %v", err)
	}
	request.Spec.Language = profilingdomain.LanguageJava
	if err := validateEnvironment(request, config); !errors.Is(err, ErrEnvironmentUnsupported) ||
		!strings.Contains(err.Error(), "bin/asprof") {
		t.Fatalf("Java environment without async-profiler: %v", err)
	}
	config.ToolDir = ""
	request.Spec.Language = profilingdomain.LanguagePython
	if err := validateEnvironment(request, config); !errors.Is(err, ErrEnvironmentUnsupported) ||
		!strings.Contains(err.Error(), "not configured") {
		t.Fatalf("Python environment without ToolDir: %v", err)
	}
	request.Spec.Language = profilingdomain.LanguageGo
	if err := validateEnvironment(request, config); err != nil {
		t.Fatalf("native environment without ToolDir: %v", err)
	}
}

func TestPythonMemoryEnvironmentRequiresGDBInsteadOfPySpy(t *testing.T) {
	config := testProfilingConfig(t)
	config.ProfilerPath = filepath.Join(t.TempDir(), "bin", "profiler")
	if err := os.MkdirAll(filepath.Dir(config.ProfilerPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.ProfilerPath, []byte("profiler fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(config.ProfilerPath, 0o700); err != nil {
		t.Fatal(err)
	}
	config.ToolDir = ""
	request := testProfilingRequest()
	request.Spec.Language = profilingdomain.LanguagePython
	request.Spec.Type = profilingdomain.TypeMemory
	request.Spec.Mode = profilingdomain.ModeObjectUsage
	binDir := t.TempDir()
	t.Setenv("PATH", binDir)
	if err := validateEnvironment(request, config); !errors.Is(err, ErrEnvironmentUnsupported) || !strings.Contains(err.Error(), "gdb") {
		t.Fatalf("environment without GDB: %v", err)
	}
	gdb := filepath.Join(binDir, "gdb")
	if err := os.WriteFile(gdb, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(gdb, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := validateEnvironment(request, config); !errors.Is(err, ErrEnvironmentUnsupported) || !strings.Contains(err.Error(), "Memray bundle") {
		t.Fatalf("environment without runtime bundle: %v", err)
	}
	bundlePath, err := memrayBundlePath(config)
	if err != nil {
		t.Fatal(err)
	}
	writeTestMemrayRuntime(t, filepath.Join(bundlePath, "python", "memray"))
	if err := validateEnvironment(request, config); err != nil {
		t.Fatalf("default Memray environment with GDB and no py-spy: %v", err)
	}
	// A configured root must not silently fall back to the healthy default.
	t.Chdir(t.TempDir())
	config.ToolDir = "separate tools"
	if err := validateEnvironment(request, config); !errors.Is(err, ErrEnvironmentUnsupported) || !strings.Contains(err.Error(), "separate tools") {
		t.Fatalf("environment without configured bundle: %v", err)
	}
	bundlePath, err = memrayBundlePath(config)
	if err != nil {
		t.Fatal(err)
	}
	packagePath := filepath.Join(bundlePath, "runtimes", "py3.12", "python", "memray")
	writeTestMemrayRuntime(t, packagePath)
	if err := validateEnvironment(request, config); err != nil {
		t.Fatalf("configured Memray environment: %v", err)
	}
	if err := os.WriteFile(filepath.Join(packagePath, "_memray.so"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateEnvironment(request, config); !errors.Is(err, ErrEnvironmentUnsupported) || !strings.Contains(err.Error(), "_memray.so") {
		t.Fatalf("environment with empty extension: %v", err)
	}
	config.ToolDir = ""
	linkedProfiler := filepath.Join(t.TempDir(), "profiler")
	if err := os.Symlink(config.ProfilerPath, linkedProfiler); err != nil {
		t.Fatal(err)
	}
	config.ProfilerPath = linkedProfiler
	if err := validateEnvironment(request, config); err != nil {
		t.Fatalf("profiler symlink did not select its actual executable's bundle: %v", err)
	}
}

func writeTestMemrayRuntime(t *testing.T, packagePath string) {
	t.Helper()
	for _, name := range []string{"__init__.py", "commands/_attach.gdb", "_memray.so", "_inject.abi3.so"} {
		path := filepath.Join(packagePath, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("runtime fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

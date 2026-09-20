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
	config.ResultPublisher = &fakeResultPublisher{}
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

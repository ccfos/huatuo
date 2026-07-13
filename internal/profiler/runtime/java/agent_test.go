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

package java

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAsyncProfilerPaths(t *testing.T) {
	t.Parallel()

	toolPath := filepath.Join("opt", "async-profiler")
	if got, want := asprofPath(toolPath), filepath.Join(toolPath, "bin", "asprof"); got != want {
		t.Fatalf("asprofPath()=%q, want %q", got, want)
	}
	if got, want := agentLibraryPath(toolPath), filepath.Join(toolPath, "lib", "libasyncProfiler.so"); got != want {
		t.Fatalf("agentLibraryPath()=%q, want %q", got, want)
	}
}

func TestCopyAgentLibUsesLibDirectory(t *testing.T) {
	t.Parallel()

	toolPath := t.TempDir()
	libDir := filepath.Join(toolPath, "lib")
	if err := os.Mkdir(libDir, 0o755); err != nil {
		t.Fatalf("Mkdir(%q) error=%v", libDir, err)
	}

	want := []byte("async-profiler-agent")
	source := filepath.Join(libDir, "libasyncProfiler.so")
	if err := os.WriteFile(source, want, 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error=%v", source, err)
	}

	targetDir := t.TempDir()
	if err := copyAgentLib(toolPath, targetDir); err != nil {
		t.Fatalf("copyAgentLib() error=%v", err)
	}

	target := filepath.Join(targetDir, "libasyncProfiler.so")
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("ReadFile(%q) error=%v", target, err)
	}
	if string(got) != string(want) {
		t.Fatalf("copied agent=%q, want %q", got, want)
	}
}

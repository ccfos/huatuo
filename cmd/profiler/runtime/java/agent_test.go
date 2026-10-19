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
	"bytes"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestAgentLibraryPath(t *testing.T) {
	t.Parallel()

	toolPath := filepath.Join("opt", "async-profiler")
	if got, want := agentLibraryPath(toolPath), filepath.Join(toolPath, "lib", "libasyncProfiler.so"); got != want {
		t.Fatalf("agentLibraryPath()=%q, want %q", got, want)
	}
}

func TestCopyAgentLibPreservesSourceMode(t *testing.T) {
	t.Parallel()

	want := []byte("async-profiler-agent")
	tests := []struct {
		name       string
		sourceMode os.FileMode
	}{
		{
			name:       "executable source",
			sourceMode: 0o755,
		},
		{
			name:       "non-executable source",
			sourceMode: 0o644,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			toolPath := t.TempDir()
			writeAgentSource(
				t,
				toolPath,
				want,
				tt.sourceMode,
			)

			targetDir := t.TempDir()
			target := filepath.Join(targetDir, "libasyncProfiler.so")

			if err := copyAgentLib(toolPath, targetDir); err != nil {
				t.Fatalf("copyAgentLib() error=%v", err)
			}

			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("ReadFile(%q) error=%v", target, err)
			}

			if !bytes.Equal(got, want) {
				t.Fatalf("copied agent=%q, want %q", got, want)
			}

			assertFileMode(t, target, tt.sourceMode)
		})
	}
}

func TestCopyAgentLibReplacesSymlink(t *testing.T) {
	t.Parallel()

	want := []byte("async-profiler-agent")
	toolPath := t.TempDir()
	writeAgentSource(
		t,
		toolPath,
		want,
		0o755,
	)

	externalPath := filepath.Join(t.TempDir(), "external")
	externalContent := []byte("do-not-overwrite")
	writeFileWithMode(
		t,
		externalPath,
		externalContent,
		0o600,
	)

	targetDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "libasyncProfiler.so")
	if err := os.Symlink(externalPath, targetPath); err != nil {
		t.Fatalf(
			"Symlink(%q, %q) error=%v",
			externalPath,
			targetPath,
			err,
		)
	}

	if err := copyAgentLib(toolPath, targetDir); err != nil {
		t.Fatalf("copyAgentLib() error=%v", err)
	}

	targetInfo, err := os.Lstat(targetPath)
	if err != nil {
		t.Fatalf("Lstat(%q) error=%v", targetPath, err)
	}

	if targetInfo.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("copyAgentLib() target mode=%v, want regular file", targetInfo.Mode())
	}

	got, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error=%v", targetPath, err)
	}

	if !bytes.Equal(got, want) {
		t.Fatalf("copied agent=%q, want %q", got, want)
	}

	externalGot, err := os.ReadFile(externalPath)
	if err != nil {
		t.Fatalf("ReadFile(%q) error=%v", externalPath, err)
	}

	if !bytes.Equal(externalGot, externalContent) {
		t.Fatalf("external file=%q, want %q", externalGot, externalContent)
	}
}

func TestCopyAgentLibRemovesTempFileAfterInstallFailure(t *testing.T) {
	t.Parallel()

	toolPath := t.TempDir()
	writeAgentSource(
		t,
		toolPath,
		[]byte("async-profiler-agent"),
		0o755,
	)

	targetDir := t.TempDir()
	targetPath := filepath.Join(targetDir, "libasyncProfiler.so")
	if err := os.Mkdir(targetPath, 0o755); err != nil {
		t.Fatalf("Mkdir(%q) error=%v", targetPath, err)
	}

	if err := copyAgentLib(toolPath, targetDir); err == nil {
		t.Fatal("copyAgentLib() error=nil, want non-nil")
	}

	assertNoAgentTempFiles(t, targetDir)
}

func TestCheckAgentDirSpaceRejectsInsufficientSpace(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := checkAgentDirSpace(dir, math.MaxUint64); err == nil {
		t.Fatalf("checkAgentDirSpace(%q, math.MaxUint64) error=nil, want non-nil", dir)
	}
}

func writeAgentSource(t *testing.T, toolPath string, content []byte, mode os.FileMode) {
	t.Helper()
	libDir := filepath.Join(toolPath, "lib")
	if err := os.Mkdir(libDir, 0o755); err != nil {
		t.Fatalf("Mkdir(%q) error=%v", libDir, err)
	}

	writeFileWithMode(
		t,
		filepath.Join(libDir, "libasyncProfiler.so"),
		content,
		mode,
	)
}

func writeFileWithMode(t *testing.T, path string, content []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatalf("WriteFile(%q) error=%v", path, err)
	}

	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("Chmod(%q) error=%v", path, err)
	}
}

func assertFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(%q) error=%v", path, err)
	}

	if got := info.Mode().Perm(); got != want.Perm() {
		t.Fatalf(
			"%s mode=%04o, want %04o",
			path,
			got,
			want.Perm(),
		)
	}
}

func assertNoAgentTempFiles(t *testing.T, dir string) {
	t.Helper()
	tempFiles, err := filepath.Glob(filepath.Join(dir, ".libasyncProfiler.so-*"))
	if err != nil {
		t.Fatalf("Glob() error=%v", err)
	}

	if len(tempFiles) != 0 {
		t.Fatalf("temporary Java agent files=%q, want none", tempFiles)
	}
}

func TestInstallJavaAgentsRollsBackPartialDeployment(t *testing.T) {
	t.Parallel()

	toolPath := t.TempDir()
	writeAgentSource(t, toolPath, []byte("agent"), 0o600)
	first, second := t.TempDir(), t.TempDir()
	// A directory at the destination forces the second atomic rename to fail.
	blocked := filepath.Join(second, "libasyncProfiler.so")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}

	installed, err := installJavaAgents(toolPath, []string{first, second})
	if err == nil || installed != nil {
		t.Fatalf("installJavaAgents() = %v, %v; want no retained handles and an error", installed, err)
	}
	if _, err := os.Stat(filepath.Join(first, "libasyncProfiler.so")); !os.IsNotExist(err) {
		t.Fatalf("first agent remains after rollback: %v", err)
	}
	if info, err := os.Stat(blocked); err != nil || !info.IsDir() {
		t.Fatalf("failed destination was removed: %v", err)
	}
	assertNoAgentTempFiles(t, first)
	assertNoAgentTempFiles(t, second)
}

func TestInstallJavaAgentsDeduplicatesDirectories(t *testing.T) {
	t.Parallel()

	toolPath := t.TempDir()
	writeAgentSource(t, toolPath, []byte("agent"), 0o600)
	parent := t.TempDir()
	directory := filepath.Join(parent, "target")
	alias := filepath.Join(parent, "alias")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(directory, alias); err != nil {
		t.Fatal(err)
	}

	installed, err := installJavaAgents(toolPath, []string{directory, alias})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, directory := range installed {
			_ = directory.Close()
		}
	})
	if len(installed) != 1 {
		t.Fatalf("retained directories = %d, want 1", len(installed))
	}
	// Like a disappearing /proc/PID path, renaming invalidates the original path.
	moved := filepath.Join(parent, "moved")
	if err := os.Rename(directory, moved); err != nil {
		t.Fatal(err)
	}
	if err := cleanupJavaAgents(installed); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, "libasyncProfiler.so")); !os.IsNotExist(err) {
		t.Fatalf("agent remains after its original directory path disappeared: %v", err)
	}
	if _, err := installed[0].Stat(); err == nil {
		t.Fatal("cleanup left the directory handle open")
	}
}

func TestCleanupJavaAgentsContinuesAfterFailure(t *testing.T) {
	t.Parallel()

	toolPath := t.TempDir()
	writeAgentSource(t, toolPath, []byte("agent"), 0o600)
	first, second := t.TempDir(), t.TempDir()
	installed, err := installJavaAgents(toolPath, []string{first, second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, directory := range installed {
			_ = directory.Close()
		}
	})
	blocked := filepath.Join(first, "libasyncProfiler.so")
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFileWithMode(t, filepath.Join(blocked, "keep"), []byte("keep"), 0o600)

	if err := cleanupJavaAgents(installed); err == nil {
		t.Fatal("cleanup ignored a removal failure")
	}
	if _, err := os.Stat(filepath.Join(second, "libasyncProfiler.so")); !os.IsNotExist(err) {
		t.Fatalf("second agent remains after cleanup: %v", err)
	}
	for _, directory := range installed {
		if _, err := directory.Stat(); err == nil {
			t.Fatal("cleanup left a directory handle open")
		}
	}
}

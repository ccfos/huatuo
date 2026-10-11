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

package golang

import (
	"context"
	"debug/elf"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestExecutableMappingIdentity(t *testing.T) {
	path := "/bin/heap (deleted)"
	exe := filepath.Join(t.TempDir(), "exe")
	if err := os.Symlink(path, exe); err != nil {
		t.Fatal(err)
	}
	mappings := []memsnapshot.ProcMap{
		{Path: "/lib/other", Inode: 42, DevMajor: 8, DevMinor: 2, Start: 0x1000},
		{Path: path, Inode: 42, DevMajor: 8, DevMinor: 3, Start: 0x8000},
	}
	mapping, err := executableMapping(exe, 42, mappings)
	if err != nil || mapping != &mappings[1] {
		t.Fatalf("executable identity = %+v, %v", mapping, err)
	}
	if _, err := executableMapping(exe, 42, mappings[:1]); err == nil {
		t.Fatal("accepted an unrelated executable path")
	}
	if _, err := executableMapping(exe, 43, mappings); err == nil {
		t.Fatal("accepted a replaced executable inode")
	}
	mappings[0].Path = path
	if _, err := executableMapping(exe, 42, mappings); err == nil {
		t.Fatal("accepted an ambiguous executable device")
	}
}

func TestNewProcessReader(t *testing.T) {
	directory := t.TempDir()
	source := filepath.Join(directory, "main.go")
	if err := os.WriteFile(source, []byte("package main\nimport (\"os\"; \"runtime\")\nfunc main(){runtime.GC(); var stop [1]byte; os.Stdin.Read(stop[:])}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "fixture")
	if output, err := exec.Command("go", "build", "-o", executable, source).CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, output)
	}
	command := exec.CommandContext(t.Context(), executable)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = command.Wait() }()
	identity, err := memsnapshot.ReadProcessInstanceID(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := newProcessReader(t.Context(), identity)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if reader.memory.pid != identity.TGID || reader.runtime.version == "" {
		t.Fatalf("reader=%+v", reader)
	}
}

func TestResolveExecutableLoadBiasNonPIE(t *testing.T) {
	file := &elf.File{FileHeader: elf.FileHeader{Type: elf.ET_EXEC}}
	// A nonexistent PID proves non-PIE resolution does not query procfs.
	bias, err := resolveExecutableLoadBias(t.Context(), -1, file)
	if err != nil || bias != 0 {
		t.Fatalf("bias=%#x error=%v", bias, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := resolveExecutableLoadBias(ctx, -1, file); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

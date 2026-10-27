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
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/memsnapshot"
)

func TestExecutableMappingIdentity(t *testing.T) {
	path := "/bin/heap (deleted)"
	exe := filepath.Join(t.TempDir(), "exe")
	if err := os.Symlink(path, exe); err != nil {
		t.Fatal(err)
	}
	mappings := []memsnapshot.ProcMap{
		{
			Path:     "/lib/other",
			Inode:    42,
			DevMajor: 8,
			DevMinor: 2,
			Start:    0x1000,
		},
		{
			Path:     path,
			Inode:    42,
			DevMajor: 8,
			DevMinor: 3,
			Start:    0x8000,
		},
	}
	segments := []elf.ProgHeader{{
		Type:   elf.PT_LOAD,
		Filesz: 0x1000,
	}}
	bias, err := executableLoadBias(exe, 42, mappings, segments)
	if err != nil || bias != 0x8000 {
		t.Fatalf("executable bias = %#x, %v", bias, err)
	}
	if _, err := executableLoadBias(exe, 42, mappings[:1], segments); err == nil {
		t.Fatal("accepted an unrelated executable path")
	}
	if _, err := executableLoadBias(exe, 43, mappings, segments); err == nil {
		t.Fatal("accepted a replaced executable inode")
	}
	mappings[0].Path = path
	if _, err := executableLoadBias(exe, 42, mappings, segments); err == nil {
		t.Fatal("accepted an ambiguous executable device")
	}
}

func TestExecutableLoadBiasInstances(t *testing.T) {
	page := uint64(os.Getpagesize())
	const path = "/bin/heap"
	const base = uint64(0x70000000)
	const linkBase = uint64(0x400000)
	exe := filepath.Join(t.TempDir(), "exe")
	if err := os.Symlink(path, exe); err != nil {
		t.Fatal(err)
	}
	// The last file page belongs to both segments with different virtual
	// offsets, as in GNU RELRO. Only unambiguous mappings nominate a bias.
	segments := []elf.ProgHeader{
		{
			Type:   elf.PT_LOAD,
			Vaddr:  linkBase,
			Filesz: 3 * page,
		},
		{
			Type:   elf.PT_LOAD,
			Off:    3*page - 0x80,
			Vaddr:  linkBase + 4*page - 0x80,
			Filesz: page + 0x80,
		},
	}
	mappingsFor := func(bias uint64) []memsnapshot.ProcMap {
		var mappings []memsnapshot.ProcMap
		for _, offset := range []uint64{0, page, 2 * page, 3 * page} {
			start := bias + linkBase + offset
			if offset == 3*page {
				start += page
			}
			mappings = append(mappings, memsnapshot.ProcMap{
				Path:     path,
				Inode:    42,
				DevMajor: 8,
				DevMinor: 1,
				Start:    start,
				End:      start + page,
				Offset:   offset,
			})
		}
		return mappings
	}
	main := mappingsFor(base)
	if bias, err := executableLoadBias(exe, 42, main, segments); err != nil || bias != base {
		t.Fatalf("split/RELRO bias=%#x, want %#x: %v", bias, base, err)
	}
	second := mappingsFor(base + 0x1000000)
	for _, mappings := range [][]memsnapshot.ProcMap{
		append(append([]memsnapshot.ProcMap{}, main...), second...),
		append(append([]memsnapshot.ProcMap{}, second...), main...),
	} {
		if _, err := executableLoadBias(exe, 42, mappings, segments); err == nil ||
			!strings.Contains(err.Error(), "multiple mapping instances") {
			t.Fatalf("accepted two load instances or returned an unrelated error: %v", err)
		}
	}
	if _, err := executableLoadBias(exe, 42, main, nil); err == nil {
		t.Fatal("accepted executable mappings without any usable load segment")
	}
}

func TestNewProcessReader(t *testing.T) {
	for _, mode := range []string{"exe", "pie", "pie-self-mmap"} {
		t.Run(mode, func(t *testing.T) {
			testNewProcessReader(t, mode)
		})
	}
}

func testNewProcessReader(t *testing.T, mode string) {
	t.Helper()
	directory := t.TempDir()
	source := filepath.Join(directory, "main.go")
	fixture := `package main
import ("os"; "runtime"; "syscall")
func main() {
 var mapping []byte
 if os.Getenv("HUATUO_TEST_MMAP_SELF") == "1" {
  file, err := os.Open("/proc/self/exe")
  if err != nil { panic(err) }
  stat, err := file.Stat()
  if err != nil { panic(err) }
  mapping, err = syscall.Mmap(int(file.Fd()), 0, int(stat.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
  if err != nil { panic(err) }
  file.Close()
 }
 runtime.GC()
 if _, err := os.Stdout.Write([]byte{'R'}); err != nil { os.Exit(1) }
 var stop [1]byte
 os.Stdin.Read(stop[:])
 runtime.KeepAlive(mapping)
}
`
	if err := os.WriteFile(source, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "fixture")
	args := []string{"build", "-o", executable}
	if mode != "exe" {
		args = append(args, "-buildmode=pie")
	}
	args = append(args, source)
	build := exec.Command("go", args...)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, output)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable)
	if mode == "pie-self-mmap" {
		command.Env = append(os.Environ(), "HUATUO_TEST_MMAP_SELF=1")
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdin.Close(); _ = command.Wait() }()
	// Start can return before exec has mapped the fixture. Wait for main
	// to finish runtime initialization before reading its memory.
	var ready [1]byte
	if _, err := io.ReadFull(stdout, ready[:]); err != nil {
		t.Fatalf("wait for fixture readiness: %v (context: %v)", err, ctx.Err())
	}
	if ready[0] != 'R' {
		t.Fatalf("unexpected fixture readiness signal: %q", ready[:])
	}
	identity, err := memsnapshot.ReadProcessInstanceID(command.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := newProcessReader(t.Context(), identity)
	if mode == "pie-self-mmap" {
		if err == nil {
			reader.Close()
			t.Fatal("accepted a second mapping of the main executable")
		}
		if !strings.Contains(err.Error(), "multiple mapping instances") {
			t.Fatalf("expected explicit load-instance ambiguity, got %v", err)
		}
		return
	}
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

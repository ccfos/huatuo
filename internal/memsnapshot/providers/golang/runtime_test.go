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
	"bytes"
	"context"
	"debug/buildinfo"
	"debug/elf"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/ccfos/huatuo/internal/symbol"
)

func runtimeExecutable(t testing.TB, unsupported bool) *os.File {
	t.Helper()
	directory := t.TempDir()
	source := filepath.Join(directory, "main.go")
	if err := os.WriteFile(source, []byte("package main\nimport (\"runtime\"; \"os\")\nfunc main() { runtime.GC(); runtime.MemProfile(nil, true); os.Stdout.Write([]byte{1}); var b [1]byte; os.Stdin.Read(b[:]) }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "fixture")
	if output, err := exec.Command("go", "build", "-o", path, source).CombinedOutput(); err != nil {
		t.Fatalf("build fixture: %v: %s", err, output)
	}
	if unsupported {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := elf.NewFile(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		section := file.Section(".go.buildinfo")
		if section == nil {
			t.Fatal("missing build info")
			return nil
		}
		offset := int(section.Offset) + 32
		size, n := binary.Uvarint(raw[offset:])
		if n <= 0 || size < 5 {
			t.Fatal("invalid inline build version")
		}
		copy(raw[offset+n:offset+n+int(size)], "go9."+strings.Repeat("9", int(size)-4))
		_ = file.Close()
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestNewRuntimeInfoUnsupportedVersion(t *testing.T) {
	file := runtimeExecutable(t, true)
	_, err := runtimeInfoFromTestExecutable(t.Context(), -1, file)
	if !errors.Is(err, errUnsupportedRuntime) || !strings.Contains(err.Error(), "unsupported Go runtime version") {
		t.Fatalf("unsupported runtime = %v", err)
	}
}

func BenchmarkNewRuntimeInfo(b *testing.B) {
	for _, unsupported := range []bool{false, true} {
		b.Run(fmt.Sprintf("unsupported=%t", unsupported), func(b *testing.B) {
			file := runtimeExecutable(b, unsupported)
			command := exec.CommandContext(b.Context(), file.Name())
			stdin, err := command.StdinPipe()
			if err != nil {
				b.Fatal(err)
			}
			stdout, err := command.StdoutPipe()
			if err != nil {
				b.Fatal(err)
			}
			if err := command.Start(); err != nil {
				b.Fatal(err)
			}
			b.Cleanup(func() { _ = stdin.Close(); _ = command.Wait() })
			var ready [1]byte
			if _, err := io.ReadFull(stdout, ready[:]); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				_, err := runtimeInfoFromTestExecutable(b.Context(), command.Process.Pid, file)
				if unsupported {
					if !errors.Is(err, errUnsupportedRuntime) {
						b.Fatal(err)
					}
				} else if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func strippedRuntimeExecutable(t testing.TB) *os.File {
	t.Helper()
	directory := t.TempDir()
	source := filepath.Join(directory, "main.go")
	code := "package main\nimport \"runtime\"\nfunc main() { runtime.MemProfile(nil, true) }\n"
	if err := os.WriteFile(source, []byte(code), 0o600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "fixture")
	command := exec.Command("go", "build", "-ldflags=-s -w", "-o", executable, source)
	command.Env = append(os.Environ(), "GOARCH=amd64", "CGO_ENABLED=0")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build stripped fixture: %v: %s", err, output)
	}
	file, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return file
}

func TestNewRuntimeInfoStrippedAMD64(t *testing.T) {
	file := strippedRuntimeExecutable(t)
	parsed, err := elf.NewFile(file)
	if err != nil {
		t.Fatal(err)
	}
	defer parsed.Close()
	table, err := symbol.ReadGoTable(t.Context(), parsed)
	if err != nil {
		t.Fatal(err)
	}
	address, err := lookupStrippedMBuckets(parsed, table)
	if err != nil || address == 0 {
		t.Fatalf("stripped mbuckets: %x, %v", address, err)
	}
	_, err = runtimeInfoFromTestExecutable(t.Context(), -1, file)
	if err == nil || !strings.Contains(err.Error(), "read runtime.mbuckets head") {
		t.Fatalf("missing process read: %v", err)
	}
}

func runtimeInfoFromTestExecutable(ctx context.Context, pid int, file *os.File) (*runtimeInfo, error) {
	parsed, err := elf.NewFile(file)
	if err != nil {
		return nil, err
	}
	defer parsed.Close()
	build, err := buildinfo.Read(file)
	if err != nil {
		return nil, err
	}
	return newRuntimeInfo(ctx, pid, parsed, build.GoVersion)
}

func TestNewRuntimeInfoCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	info, err := newRuntimeInfo(ctx, -1, nil, "")
	if info != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled construction: %v %v", info, err)
	}
}

func TestNewRuntimeInfoRelocationFailure(t *testing.T) {
	executable := runtimeExecutable(t, false)
	file, err := elf.NewFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	// Force process mapping resolution after otherwise successful ELF inspection.
	file.Type = elf.ET_DYN
	info, err := newRuntimeInfo(t.Context(), -1, file, "go1.26.0")
	if info != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial metadata escaped: %v %v", info, err)
	}
}

func TestNewRuntimeInfoUnsupportedStrippedArchitecture(t *testing.T) {
	file := &elf.File{FileHeader: elf.FileHeader{Class: elf.ELFCLASS64, Machine: elf.EM_AARCH64}}
	info, err := newRuntimeInfo(t.Context(), -1, file, "go1.26.0")
	if info != nil || !errors.Is(err, errMBucketsSymbolNotFound) || !strings.Contains(err.Error(), "stripped EM_AARCH64") {
		t.Fatalf("unsupported stripped runtime: %v %v", info, err)
	}
}

func TestNewRuntimeInfoPreservesSymbolError(t *testing.T) {
	file := &elf.File{
		FileHeader: elf.FileHeader{Class: elf.ELFCLASS64, Machine: elf.EM_AARCH64},
		Sections:   []*elf.Section{{SectionHeader: elf.SectionHeader{Type: elf.SHT_SYMTAB, Entsize: 1}}},
	}
	_, err := newRuntimeInfo(t.Context(), -1, file, "go1.26.0")
	if !errors.Is(err, errMBucketsSymbolNotFound) || !strings.Contains(err.Error(), "invalid or oversized ELF symbol table") {
		t.Fatalf("lost symbol read failure: %v", err)
	}
}

func TestReadMemProfileRate(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, rate := range []int64{-2, 0, 1, 512 * 1024} {
			var raw [8]byte
			order.PutUint64(raw[:], uint64(rate))
			var pin runtime.Pinner
			pin.Pin(&raw[0])
			got := (&runtimeInfo{layout: runtimeLayout{byteOrder: order}}).readMemProfileRate(os.Getpid(), uint64(uintptr(unsafe.Pointer(&raw[0]))))
			pin.Unpin()
			want := rate
			if want < 0 {
				want = -1
			}
			if got != want {
				t.Fatalf("rate %d: got %d", rate, got)
			}
		}
	}
	got := (&runtimeInfo{layout: runtimeLayout{byteOrder: binary.LittleEndian}}).readMemProfileRate(os.Getpid(), 1)
	if got != -1 {
		t.Fatalf("unreadable rate: %d", got)
	}
}

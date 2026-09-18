// Copyright 2023 Odigos
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
//
// Adapted from odigos-io/odigos procdiscovery commit
// 7c6279dd7530a0fd3cdb3d21829c06d65445ff70.

package memsnap

import (
	"bytes"
	"context"
	"debug/elf"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Language identifies a process runtime.
type Language string

// Supported process runtimes recognized by language detection.
const (
	LanguageUnknown Language = "unknown"
	LanguageJava    Language = "java"
	LanguageGo      Language = "go"
	LanguagePython  Language = "python"
)

var pythonExecutablePattern = regexp.MustCompile(`^python(\d+(\.\d+)?)?$`)

// DetectLanguage identifies a runtime by reading /proc/<pid>. It first
// checks the executable for readable Go build information, then the executable
// basename, then the mapped runtime libraries.
//
// Cancellation is cooperative because the procfs and ELF APIs expose only
// synchronous Read and ReadAt calls. The deadline is therefore a cooperative
// stop budget, not a wall-clock upper bound. It cannot preempt a syscall that
// has entered the kernel, and that syscall may block without a time bound. The
// readers below check the context both before and after each call, so once the
// in-flight call returns, cancellation prevents subsequent parsing I/O from
// starting. A hard bound would require isolating reads in a separately managed
// process.
func DetectLanguage(ctx context.Context, pid int) (Language, error) {
	return detectLanguage(ctx, procPath(pid, "exe"), procPath(pid, "maps"))
}

func detectLanguage(ctx context.Context, exePath, mapsPath string) (Language, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return LanguageUnknown, err
	}
	exeFile, err := os.Open(exePath)
	if err != nil {
		return LanguageUnknown, fmt.Errorf("open executable: %w", err)
	}
	defer exeFile.Close()
	executable, err := ReadELFMetadata(ctx, exeFile)
	if err != nil {
		return LanguageUnknown, fmt.Errorf("inspect executable ELF: %w", err)
	}
	// Inspect only the fixed Go build-info magic, never target-sized strings.
	if section := executable.Section(".go.buildinfo"); section != nil && section.Size >= 14 {
		var magic [14]byte
		if _, err := (contextReaderAt{ctx: ctx, reader: exeFile}).ReadAt(magic[:], int64(section.Offset)); err != nil {
			return LanguageUnknown, fmt.Errorf("read Go build information: %w", err)
		}
		if bytes.Equal(magic[:], []byte("\xff Go buildinf:")) {
			return LanguageGo, nil
		}
	}
	name, err := os.Readlink(exePath)
	if err != nil {
		return LanguageUnknown, fmt.Errorf("read executable link: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return LanguageUnknown, err
	}
	if detected := languageFromExecutable(filepath.Base(name)); detected != LanguageUnknown {
		return detected, nil
	}
	mappings, err := ReadProcMapsContext(ctx, mapsPath, 4096)
	if err != nil {
		return LanguageUnknown, fmt.Errorf("inspect runtime maps: %w", err)
	}
	java := false
	for _, mapping := range mappings {
		java = java || strings.HasSuffix(strings.TrimSuffix(mapping.Path, " (deleted)"), "/libjvm.so")
	}
	python, err := elfLinksPython(ctx, executable)
	if err != nil {
		return LanguageUnknown, fmt.Errorf("inspect executable dependencies: %w", err)
	}
	switch {
	case java && python:
		return LanguageUnknown, nil
	case java:
		return LanguageJava, nil
	case python:
		return LanguagePython, nil
	default:
		return LanguageUnknown, nil
	}
}

func languageFromExecutable(executable string) Language {
	switch {
	case executable == "java":
		return LanguageJava
	case pythonExecutablePattern.MatchString(executable):
		return LanguagePython
	default:
		return LanguageUnknown
	}
}

// These adapters provide cooperative cancellation around synchronous process
// reads. A context cannot interrupt a Read or ReadAt syscall that has already
// entered the kernel, so cancellation is checked both before and after every
// call. The in-flight syscall may block without a time bound; after it returns,
// no later read is started. This is intentionally not a hard per-syscall
// deadline, which the io.Reader interfaces cannot provide.
type contextReaderAt struct {
	ctx    context.Context
	reader io.ReaderAt
}

func (r contextReaderAt) ReadAt(p []byte, offset int64) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.reader.ReadAt(p, offset)
	if ctxErr := r.ctx.Err(); ctxErr != nil {
		return n, ctxErr
	}
	return n, err
}

func procPath(pid int, name string) string {
	return fmt.Sprintf("/proc/%d/%s", pid, name)
}

// Read only bounded dynamic metadata, without DynString's potentially repeated
// string allocations from attacker-controlled DT_NEEDED entries.
func elfLinksPython(ctx context.Context, file *elf.File) (bool, error) {
	dynamic := file.SectionByType(elf.SHT_DYNAMIC)
	if dynamic == nil {
		return false, ctx.Err()
	}
	if strings.HasPrefix(dynamic.Name, ".zdebug") || dynamic.Flags&elf.SHF_COMPRESSED != 0 || dynamic.Size > 64<<10 || dynamic.Link == 0 || uint64(dynamic.Link) >= uint64(len(file.Sections)) {
		return false, fmt.Errorf("ELF dynamic table exceeds detection budget or has invalid link")
	}
	table := file.Sections[dynamic.Link]
	if strings.HasPrefix(table.Name, ".zdebug") || table.Type != elf.SHT_STRTAB || table.Flags&elf.SHF_COMPRESSED != 0 || table.Size > maxDetectionELFBytes {
		return false, fmt.Errorf("ELF dependency string table is invalid or exceeds detection budget")
	}
	data, err := dynamic.Data()
	if err != nil {
		return false, err
	}
	names, err := table.Data()
	if err != nil {
		return false, err
	}
	entrySize := 8
	if file.Class == elf.ELFCLASS64 {
		entrySize = 16
	}
	if len(data)%entrySize != 0 {
		return false, fmt.Errorf("malformed ELF dynamic table")
	}
	found := false
	for offset := 0; offset < len(data); offset += entrySize {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		var tag, value uint64
		if entrySize == 16 {
			tag, value = file.ByteOrder.Uint64(data[offset:]), file.ByteOrder.Uint64(data[offset+8:])
		} else {
			tag, value = uint64(file.ByteOrder.Uint32(data[offset:])), uint64(file.ByteOrder.Uint32(data[offset+4:]))
		}
		if tag == uint64(elf.DT_NULL) {
			break
		}
		if tag != uint64(elf.DT_NEEDED) {
			continue
		}
		if value >= uint64(len(names)) {
			return false, fmt.Errorf("invalid ELF dependency offset")
		}
		name := names[value:]
		if len(name) > 4096 {
			name = name[:4096]
		}
		end := bytes.IndexByte(name, 0)
		if end < 0 {
			return false, fmt.Errorf("ELF dependency name exceeds detection budget")
		}
		found = found || bytes.Contains(name[:end], []byte("libpython3"))
	}
	return found, ctx.Err()
}

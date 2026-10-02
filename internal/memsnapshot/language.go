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

package memsnapshot

import (
	"bytes"
	"context"
	"debug/elf"
	"fmt"
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

const maxDependencyStringTableBytes = 8 << 20

var pythonExecutablePattern = regexp.MustCompile(`^python(\d+(\.\d+)?)?$`)

// DetectLanguage identifies a runtime by reading /proc/<pid>. It first
// checks the executable for readable Go build information, then the executable
// basename, then the mapped runtime libraries.
//
// Reads are synchronous; callers can check cancellation and elapsed time only
// after detection returns.
func DetectLanguage(pid int) (Language, error) {
	return detectLanguage(procPath(pid, "exe"), procPath(pid, "maps"))
}

func detectLanguage(exePath, mapsPath string) (Language, error) {
	exeFile, err := os.Open(exePath)
	if err != nil {
		return LanguageUnknown, fmt.Errorf("open executable: %w", err)
	}
	defer exeFile.Close()
	executable, err := elf.NewFile(exeFile)
	if err != nil {
		return LanguageUnknown, fmt.Errorf("inspect executable ELF: %w", err)
	}
	// Inspect only the fixed Go build-info magic, never target-sized strings.
	if section := executable.Section(".go.buildinfo"); section != nil && section.Size >= 14 {
		var magic [14]byte
		if _, err := exeFile.ReadAt(magic[:], int64(section.Offset)); err != nil {
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
	if detected := languageFromExecutable(filepath.Base(name)); detected != LanguageUnknown {
		return detected, nil
	}
	mappings, err := ReadProcMapsContext(context.Background(), mapsPath, 4096)
	if err != nil {
		return LanguageUnknown, fmt.Errorf("inspect runtime maps: %w", err)
	}
	java := false
	for _, mapping := range mappings {
		java = java || strings.HasSuffix(strings.TrimSuffix(mapping.Path, " (deleted)"), "/libjvm.so")
	}
	python, err := elfLinksPython(executable)
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

func procPath(pid int, name string) string {
	return fmt.Sprintf("/proc/%d/%s", pid, name)
}

// Read only bounded dynamic metadata, without DynString's potentially repeated
// string allocations from attacker-controlled DT_NEEDED entries.
func elfLinksPython(file *elf.File) (bool, error) {
	dynamic := file.SectionByType(elf.SHT_DYNAMIC)
	if dynamic == nil {
		return false, nil
	}
	if strings.HasPrefix(dynamic.Name, ".zdebug") || dynamic.Flags&elf.SHF_COMPRESSED != 0 || dynamic.Size > 64<<10 || dynamic.Link == 0 || uint64(dynamic.Link) >= uint64(len(file.Sections)) {
		return false, fmt.Errorf("ELF dynamic table exceeds detection budget or has invalid link")
	}
	table := file.Sections[dynamic.Link]
	if strings.HasPrefix(table.Name, ".zdebug") || table.Type != elf.SHT_STRTAB || table.Flags&elf.SHF_COMPRESSED != 0 || table.Size > maxDependencyStringTableBytes {
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
	return found, nil
}

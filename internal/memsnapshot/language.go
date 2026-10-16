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
	LanguageUnknown    Language = "unknown"
	LanguageJava       Language = "java"
	LanguageGo         Language = "go"
	LanguagePython     Language = "python"
	LanguageCPP        Language = "cpp"
	LanguageNative     Language = "native"
	LanguageRust       Language = "rust"
	LanguageJavaScript Language = "javascript"
	LanguageRuby       Language = "ruby"
	LanguagePHP        Language = "php"
	LanguageDotNet     Language = "dotnet"
)

const (
	maxDependencyStringTableBytes = 8 << 20
	maxLanguageSymbolBytes        = 8 << 20
	maxLanguageSymbols            = 65536
)

var (
	rubyExecutablePattern = regexp.MustCompile(`^(ruby|rails|rails server|rake|rackup|puma|unicorn|gem|bundler|irb|pry)(\d+(\.\d+)*)?$`)
	phpExecutablePattern  = regexp.MustCompile(`^php(-cgi|-fpm)?[0-9.]*$`)
	phpLibraryPattern     = regexp.MustCompile(`^(libphp|mod_php|php)[0-9.]*\.so(\.\d+)*$`)
	pythonLibraryPattern  = regexp.MustCompile(`^libpython3(\.\d+)*(t?d?m?u?)\.so(\.\d+)*$`)
)

var pythonExecutablePattern = regexp.MustCompile(`^(platform-)?python(\d+(\.\d+)?(t?d?m?u?))?$`)

// DetectLanguage identifies a runtime by reading /proc/<pid>. It first
// checks the executable for readable Go build information, then the executable
// basename, mapped runtime libraries, and main-executable dependencies/symbols.
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
	if detected := languageFromExecutable(filepath.Base(strings.TrimSuffix(name, " (deleted)"))); detected != LanguageUnknown {
		return detected, nil
	}
	mappings, err := ReadProcMapsContext(context.Background(), mapsPath, 4096)
	if err != nil {
		return LanguageUnknown, fmt.Errorf("inspect runtime maps: %w", err)
	}
	python, cpp, err := elfDependencies(executable)
	if err != nil {
		return LanguageUnknown, fmt.Errorf("inspect executable dependencies: %w", err)
	}
	// Confirm managed runtimes before interpreting C++ components as the main language.
	detected := map[Language]bool{LanguagePython: python}
	for _, mapping := range mappings {
		path := strings.TrimSuffix(mapping.Path, " (deleted)")
		library := filepath.Base(path)
		detected[LanguageJava] = detected[LanguageJava] || matchesRuntimeLibrary(library, "libjvm.so")
		detected[LanguagePython] = detected[LanguagePython] || pythonLibraryPattern.MatchString(library)
		detected[LanguageJavaScript] = detected[LanguageJavaScript] || matchesRuntimeLibrary(library, "libnode.so")
		detected[LanguageDotNet] = detected[LanguageDotNet] || matchesRuntimeLibrary(library, "libcoreclr.so")
		detected[LanguageRuby] = detected[LanguageRuby] || matchesRuntimeLibrary(library, "libruby.so")
		detected[LanguagePHP] = detected[LanguagePHP] || phpLibraryPattern.MatchString(library)
	}
	result := LanguageUnknown
	for _, language := range []Language{LanguageJava, LanguagePython, LanguageJavaScript, LanguageDotNet, LanguageRuby, LanguagePHP} {
		if !detected[language] {
			continue
		}
		if result != LanguageUnknown {
			return LanguageUnknown, fmt.Errorf("conflicting runtime evidence: %s and %s", result, language)
		}
		result = language
	}
	if result != LanguageUnknown {
		return result, nil
	}
	// Main-executable symbols and direct dependencies are evidence for C++.
	// A C++ library in maps alone may belong to an extension or plugin.
	symbols, err := languageFromSymbols(executable)
	if err != nil {
		return LanguageUnknown, fmt.Errorf("inspect executable symbols: %w", err)
	}
	if symbols == LanguageRust {
		return symbols, nil
	}
	if cpp || symbols == LanguageCPP {
		return LanguageCPP, nil
	}
	return LanguageNative, nil
}

func matchesRuntimeLibrary(name, library string) bool {
	if name == library {
		return true
	}
	suffix, ok := strings.CutPrefix(name, library+".")
	if !ok || suffix == "" {
		return false
	}
	for _, part := range strings.Split(suffix, ".") {
		if part == "" {
			return false
		}
		for _, digit := range part {
			if digit < '0' || digit > '9' {
				return false
			}
		}
	}
	return true
}

func languageFromExecutable(executable string) Language {
	switch {
	case executable == "node" || executable == "nodejs" || executable == "npm" || executable == "npx" || executable == "yarn":
		return LanguageJavaScript
	case rubyExecutablePattern.MatchString(executable):
		return LanguageRuby
	case phpExecutablePattern.MatchString(executable):
		return LanguagePHP
	case executable == "dotnet":
		return LanguageDotNet
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
func elfDependencies(file *elf.File) (python, cpp bool, err error) {
	dynamic := file.SectionByType(elf.SHT_DYNAMIC)
	if dynamic == nil {
		return false, false, nil
	}
	if strings.HasPrefix(dynamic.Name, ".zdebug") || dynamic.Flags&elf.SHF_COMPRESSED != 0 || dynamic.Size > 64<<10 || dynamic.Link == 0 || uint64(dynamic.Link) >= uint64(len(file.Sections)) {
		return false, false, fmt.Errorf("ELF dynamic table exceeds detection budget or has invalid link")
	}
	table := file.Sections[dynamic.Link]
	if strings.HasPrefix(table.Name, ".zdebug") || table.Type != elf.SHT_STRTAB || table.Flags&elf.SHF_COMPRESSED != 0 || table.Size > maxDependencyStringTableBytes {
		return false, false, fmt.Errorf("ELF dependency string table is invalid or exceeds detection budget")
	}
	data, err := dynamic.Data()
	if err != nil {
		return false, false, err
	}
	names, err := table.Data()
	if err != nil {
		return false, false, err
	}
	entrySize := 8
	if file.Class == elf.ELFCLASS64 {
		entrySize = 16
	}
	if len(data)%entrySize != 0 {
		return false, false, fmt.Errorf("malformed ELF dynamic table")
	}
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
			return false, false, fmt.Errorf("invalid ELF dependency offset")
		}
		name := names[value:]
		if len(name) > 4096 {
			name = name[:4096]
		}
		end := bytes.IndexByte(name, 0)
		if end < 0 {
			return false, false, fmt.Errorf("ELF dependency name exceeds detection budget")
		}
		python = python || pythonLibraryPattern.Match(name[:end])
		library := string(name[:end])
		cpp = cpp || matchesRuntimeLibrary(library, "libstdc++.so") || matchesRuntimeLibrary(library, "libc++.so") || matchesRuntimeLibrary(library, "libc++abi.so")
	}
	return python, cpp, nil
}

// Scan bounded main-executable symbols without allocating all symbol names.
// No symbol table is an ordinary non-match; unreadable metadata is an error.
func languageFromSymbols(file *elf.File) (Language, error) {
	entrySize := uint64(16)
	if file.Class == elf.ELFCLASS64 {
		entrySize = 24
	}
	var total, count uint64
	for _, section := range file.Sections {
		if section.Type != elf.SHT_SYMTAB && section.Type != elf.SHT_DYNSYM {
			continue
		}
		if strings.HasPrefix(section.Name, ".zdebug") || section.Flags&elf.SHF_COMPRESSED != 0 || section.Entsize != entrySize || section.Size%entrySize != 0 || section.Size > maxLanguageSymbolBytes || uint64(section.Link) >= uint64(len(file.Sections)) {
			return LanguageUnknown, fmt.Errorf("invalid or oversized symbol table")
		}
		names := file.Sections[section.Link]
		if strings.HasPrefix(names.Name, ".zdebug") || names.Type != elf.SHT_STRTAB || names.Flags&elf.SHF_COMPRESSED != 0 || names.Size > maxLanguageSymbolBytes {
			return LanguageUnknown, fmt.Errorf("invalid or oversized symbol string table")
		}
		total += section.Size + names.Size
		count += section.Size / entrySize
		if total > maxLanguageSymbolBytes || count > maxLanguageSymbols {
			return LanguageUnknown, fmt.Errorf("symbol metadata exceeds detection budget")
		}
	}
	result := LanguageUnknown
	var scanned uint64
	for _, section := range file.Sections {
		if section.Type != elf.SHT_SYMTAB && section.Type != elf.SHT_DYNSYM {
			continue
		}
		symbols, err := section.Data()
		if err != nil {
			return LanguageUnknown, err
		}
		names, err := file.Sections[section.Link].Data()
		if err != nil {
			return LanguageUnknown, err
		}
		if uint64(len(symbols)) != section.Size || uint64(len(symbols))%entrySize != 0 {
			return LanguageUnknown, fmt.Errorf("invalid decoded symbol table %s", section.Name)
		}
		if uint64(len(names)) != file.Sections[section.Link].Size {
			return LanguageUnknown, fmt.Errorf("invalid decoded symbol string table")
		}
		for offset := uint64(0); offset < uint64(len(symbols)); offset += entrySize {
			start := file.ByteOrder.Uint32(symbols[offset:])
			if uint64(start) >= uint64(len(names)) {
				return LanguageUnknown, fmt.Errorf("invalid symbol name offset")
			}
			name := names[start:]
			if len(name) > 4096 {
				name = name[:4096]
			}
			end := bytes.IndexByte(name, 0)
			if end < 0 {
				return LanguageUnknown, fmt.Errorf("symbol name exceeds detection budget")
			}
			name = name[:end]
			scanned += uint64(end) + 1
			if scanned > maxLanguageSymbolBytes {
				return LanguageUnknown, fmt.Errorf("symbol name scan exceeds detection budget")
			}
			if bytes.Contains(name, []byte("__rust_")) {
				result = LanguageRust
			} else if result != LanguageRust && (bytes.Equal(name, []byte("__cxa_throw")) || bytes.Equal(name, []byte("__cxa_begin_catch")) || bytes.Equal(name, []byte("__cxa_allocate_exception")) || bytes.HasPrefix(name, []byte("_ZTV")) || bytes.HasPrefix(name, []byte("_ZTI"))) {
				result = LanguageCPP
			}
		}
	}
	return result, nil
}

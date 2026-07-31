/*
 * Copyright 2023 Odigos
 * Copyright 2026 The HuaTuo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Adapted from odigos-io/odigos procdiscovery commit
 * 7c6279dd7530a0fd3cdb3d21829c06d65445ff70.
 */

/* Package processlang detects process runtime languages. */
package processlang

import (
	"bufio"
	"debug/buildinfo"
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

/* Language identifies a process runtime. */
type Language string

const (
	LanguageUnknown Language = "unknown"
	LanguageJava    Language = "java"
	LanguageGo      Language = "go"
	LanguagePython  Language = "python"
	LanguageNode    Language = "node"
)

/* ProcessDetails contains live or captured process metadata. */
type ProcessDetails struct {
	PID     int
	Comm    string
	Cmdline string
}

var pythonExecutablePattern = regexp.MustCompile(`^python(\d+(\.\d+)?)?$`)

/* DetectLanguage detects a runtime from a live PID or captured metadata. */
func DetectLanguage(details ProcessDetails) Language {
	if details.PID <= 0 {
		return languageFromMetadata(details.Cmdline, details.Comm)
	}

	return languageFromRunningProcess(details.PID)
}

func languageFromRunningProcess(pid int) Language {
	exeFile, _ := os.Open(procPath(pid, "exe"))
	if exeFile != nil {
		defer exeFile.Close()

		/* Go binaries contain readable Go build information. */
		if _, err := buildinfo.Read(exeFile); err == nil {
			return LanguageGo
		}
	}

	exePath, _ := os.Readlink(procPath(pid, "exe"))
	detected := languageFromExecutable(filepath.Base(exePath))
	if detected != LanguageUnknown {
		return detected
	}

	return languageFromRuntimeLibraries(pid, exeFile)
}

func languageFromRuntimeLibraries(pid int, exeFile *os.File) Language {
	java := mapsContain(pid, "/libjvm.so")
	python := exeFile != nil && linksPython(exeFile)
	switch {
	case java && python:
		return LanguageUnknown
	case java:
		return LanguageJava
	case python:
		return LanguagePython
	default:
		return LanguageUnknown
	}
}

func languageFromMetadata(cmdline, comm string) Language {
	detected := languageFromCmdline(cmdline)
	if detected == LanguageUnknown {
		detected = languageFromExecutable(comm)
	}
	return detected
}

func languageFromCmdline(cmdline string) Language {
	fields := strings.Fields(cmdline)
	if len(fields) == 0 {
		return LanguageUnknown
	}

	detected := languageFromExecutable(filepath.Base(fields[0]))
	if detected != LanguageUnknown || filepath.Base(fields[0]) != "env" {
		return detected
	}
	for _, field := range fields[1:] {
		if strings.HasPrefix(field, "-") || strings.Contains(field, "=") {
			continue
		}
		return languageFromExecutable(filepath.Base(field))
	}
	return LanguageUnknown
}

func languageFromExecutable(executable string) Language {
	switch {
	case executable == "java":
		return LanguageJava
	case pythonExecutablePattern.MatchString(executable):
		return LanguagePython
	case executable == "node", executable == "npm", executable == "npx", executable == "yarn":
		return LanguageNode
	default:
		return LanguageUnknown
	}
}

func mapsContain(pid int, substring string) bool {
	file, err := os.Open(procPath(pid, "maps"))
	if err != nil {
		return false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if strings.Contains(scanner.Text(), substring) {
			return true
		}
	}
	return false
}

func linksPython(file *os.File) bool {
	executable, err := elf.NewFile(file)
	if err != nil {
		return false
	}
	dependencies, err := executable.DynString(elf.DT_NEEDED)
	if err != nil {
		return false
	}
	for _, dependency := range dependencies {
		if strings.Contains(dependency, "libpython3") {
			return true
		}
	}
	return false
}

func procPath(pid int, name string) string {
	return fmt.Sprintf("/proc/%d/%s", pid, name)
}

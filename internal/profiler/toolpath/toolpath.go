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

// Package toolpath owns the installed layout of external profiling tools.
package toolpath

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ccfos/huatuo/pkg/profiling"
)

// Validate checks only the requested language's tools within directory.
// Native profiling does not require external tools.
func Validate(language profiling.Language, directory string) error {
	switch language {
	case profiling.LanguageC, profiling.LanguageCPP, profiling.LanguageGo:
		return nil
	case profiling.LanguageJava, profiling.LanguagePython:
		if directory == "" {
			return fmt.Errorf("%s profiling tool directory is not configured", language)
		}
	default:
		return fmt.Errorf("validate profiling tools: unsupported language %q", language)
	}

	if language == profiling.LanguagePython {
		return validateFile(filepath.Join(directory, "py-spy"), 0o111)
	}
	if err := validateFile(filepath.Join(directory, "bin", "asprof"), 0o111); err != nil {
		return err
	}
	return validateFile(filepath.Join(directory, "lib", "libasyncProfiler.so"), 0o444)
}

func validateFile(path string, permissions os.FileMode) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("profiling tool %q is unavailable: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("profiling tool %q is not a regular file", path)
	}
	if info.Mode().Perm()&permissions == 0 {
		return fmt.Errorf("profiling tool %q requires permission bits %#o", path, permissions)
	}
	return nil
}

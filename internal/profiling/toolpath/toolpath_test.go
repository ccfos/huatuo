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

package toolpath

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/pkg/profiling"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name      string
		language  profiling.Language
		files     map[string]os.FileMode
		wantError string
	}{
		{
			name: "java without python", language: profiling.LanguageJava,
			files: map[string]os.FileMode{
				"bin/asprof":              0o700,
				"lib/libasyncProfiler.so": 0o600,
			},
		},
		{
			name: "python without java", language: profiling.LanguagePython,
			files: map[string]os.FileMode{"py-spy": 0o700},
		},
		{name: "missing java binary", language: profiling.LanguageJava, wantError: "bin/asprof"},
		{
			name: "missing java library", language: profiling.LanguageJava,
			files:     map[string]os.FileMode{"bin/asprof": 0o700},
			wantError: "lib/libasyncProfiler.so",
		},
		{
			name: "java library unreadable", language: profiling.LanguageJava,
			files: map[string]os.FileMode{
				"bin/asprof":              0o700,
				"lib/libasyncProfiler.so": 0o000,
			},
			wantError: "requires permission bits",
		},
		{
			name: "python binary not executable", language: profiling.LanguagePython,
			files:     map[string]os.FileMode{"py-spy": 0o600},
			wantError: "requires permission bits",
		},
		{
			name: "python binary is a directory", language: profiling.LanguagePython,
			files:     map[string]os.FileMode{"py-spy": os.ModeDir | 0o700},
			wantError: "not a regular file",
		},
		{
			name: "reject nested java layout", language: profiling.LanguageJava,
			files: map[string]os.FileMode{
				"async-profiler/bin/asprof":              0o700,
				"async-profiler/lib/libasyncProfiler.so": 0o600,
			},
			wantError: "bin/asprof",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			for name, mode := range tt.files {
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if mode.IsDir() {
					if err := os.Mkdir(path, mode.Perm()); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(path, mode); err != nil {
					t.Fatal(err)
				}
			}
			err := Validate(tt.language, root)
			if tt.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantError) {
					t.Fatalf("Validate() error = %v, want %q", err, tt.wantError)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateRequiresRootForExternalTools(t *testing.T) {
	for _, language := range []profiling.Language{profiling.LanguageJava, profiling.LanguagePython} {
		t.Run(string(language), func(t *testing.T) {
			if err := Validate(language, ""); err == nil || !strings.Contains(err.Error(), "not configured") {
				t.Fatalf("Validate() error = %v, want unconfigured tool directory", err)
			}
		})
	}
}

func TestValidateNativeNeedsNoTools(t *testing.T) {
	for _, language := range []profiling.Language{profiling.LanguageC, profiling.LanguageCPP, profiling.LanguageGo} {
		if err := Validate(language, ""); err != nil {
			t.Errorf("Validate(%q) error = %v, want no error", language, err)
		}
	}
}

func TestValidatePreservesMissingFileError(t *testing.T) {
	err := Validate(profiling.LanguagePython, t.TempDir())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Validate() error = %v, want os.ErrNotExist", err)
	}
}

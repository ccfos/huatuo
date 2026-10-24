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

//go:build integration

package bpfmock

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestKernelHookDeclarations(t *testing.T) {
	gcc, err := exec.LookPath("gcc")
	if err != nil {
		t.Skip("gcc is required")
	}
	for _, tc := range []struct {
		name, declaration string
		inputs            int
	}{
		{"unnamed parameters", "void target(struct page *, unsigned long, bool);", 1},
		{"named parameters", "void target(struct page *page, unsigned long address, bool compound);", 1},
		{"multiline and extra pointer", "extern unsigned long target(struct page *page,\n unsigned long len, const struct page **pages);", 2},
		{"aggregate flags", "unsigned long target(struct page *page, unsigned long len, flags_t flags);", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "asm volatile(\"\" ::: \"memory\");"
			if strings.Contains(tc.declaration, "unsigned long target") {
				body += " return 0"
			}
			cmd := exec.Command("awk", "-v", "symbol=target", "-v", "inputs="+strconv.Itoa(tc.inputs), "-v", "body="+body, "-f", "kernel_hook.awk")
			cmd.Stdin = strings.NewReader(tc.declaration + "\n")
			generated, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("generate: %v: %s", err, generated)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "hook.c")
			arguments := "&page"
			if tc.inputs == 2 {
				arguments += ", 4096"
			}
			source := "#include <stdbool.h>\n#define noinline __attribute__((noinline))\n#define __noclone __attribute__((noclone))\nstruct page { int value; }; typedef struct { unsigned long bits[4]; } flags_t;\n" + tc.declaration + "\n" + string(generated) + "\nint main(void) { struct page page; CALL_target(" + arguments + "); return 0; }\n"
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := exec.Command(gcc, "-std=gnu11", "-Wall", "-Werror", "-fsyntax-only", path).CombinedOutput()
			if err != nil {
				t.Fatalf("compile: %v: %s\n%s", err, output, source)
			}
		})
	}
}

func TestKernelHookRejectsMissingOrUnsupportedDeclarations(t *testing.T) {
	for _, tc := range []struct {
		name, declaration string
		optional, success bool
	}{
		{"missing", "void other(void);", false, false},
		{"optional", "void other(void);", true, true},
		{"incomplete", "void target(struct page *page,", false, false},
		{"callback", "void target(void (*callback)(void));", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"-v", "symbol=target", "-f", "kernel_hook.awk"}
			if tc.optional {
				args = append([]string{"-v", "optional=1"}, args...)
			}
			cmd := exec.Command("awk", args...)
			cmd.Stdin = strings.NewReader(tc.declaration + "\n")
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.success {
				t.Fatalf("generate: %v: %s", err, out)
			}
		})
	}
}

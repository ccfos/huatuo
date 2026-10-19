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

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReclaimHookSignature(t *testing.T) {
	for _, tool := range []string{"make", "gcc", "awk"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is required for the kernel declaration test", tool)
		}
	}
	kbuild, err := filepath.Abs("../Kbuild")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, header, qualifier string
	}{
		{name: "public mutable", header: "include/linux/swap.h"},
		{name: "public const", header: "include/linux/swap.h", qualifier: "const "},
		{name: "private const", header: "mm/internal.h", qualifier: "const "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			header := filepath.Join(workspace, tc.header)
			if err := os.MkdirAll(filepath.Dir(header), 0o700); err != nil {
				t.Fatal(err)
			}
			declaration := "extern unsigned long try_to_free_pages(struct zonelist *zonelist, int order,\n gfp_t gfp_mask, " + tc.qualifier + "nodemask_t *mask);\n"
			if err := os.WriteFile(header, []byte(declaration), 0o600); err != nil {
				t.Fatal(err)
			}
			generated := filepath.Join(workspace, "memory_reclaim_events_hook.h")
			cmd := exec.Command("make", "-s", "-f", kbuild, "srctree="+workspace, "obj="+workspace, generated)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("derive hook declaration: %v: %s", err, output)
			}
			source := "struct zonelist; typedef unsigned gfp_t; typedef int nodemask_t;\n" + declaration + `
#include "memory_reclaim_events_hook.h"
unsigned long try_to_free_pages_mock RECLAIM_MOCK_HOOK_ARGS;
_Static_assert(__builtin_types_compatible_p(__typeof__(try_to_free_pages), __typeof__(try_to_free_pages_mock)), "hook signature changed");
`
			cmd = exec.Command("gcc", "-Wall", "-Werror", "-I", workspace, "-x", "c", "-c", "-o", filepath.Join(workspace, "signature.o"), "-")
			cmd.Stdin = strings.NewReader(source)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("mock signature differs: %v: %s", err, output)
			}
		})
	}
}

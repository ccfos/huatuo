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

package symbol

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestUsymResolverReloadsExecutableCache(t *testing.T) {
	setTestXfsMounts(t, []string{"/"})
	tmpRoot := setupTempProcRoot(t)
	const processID = uint32(1001)
	procDir := filepath.Join(tmpRoot, "proc", strconv.Itoa(int(processID)))
	rootTarget := filepath.Join(tmpRoot, "container-root")
	executablePath := filepath.Join(rootTarget, "usr", "bin", "huatuo-dev")

	mustMkdirAll(t, procDir)
	mustMkdirAll(t, filepath.Dir(executablePath))
	mustSymlink(t, rootTarget, filepath.Join(procDir, "root"))
	mustSymlink(t, "/usr/bin/huatuo-dev", filepath.Join(procDir, "exe"))
	copyCurrentExecutable(t, executablePath)

	resolver := NewUsymResolver()
	first, err := resolver.loadElfCaches(processID)
	if err != nil {
		t.Fatal(err)
	}
	resolver.procmaps[processID] = sections{{}}

	replacement := filepath.Join(tmpRoot, "replacement")
	copyCurrentExecutable(t, replacement)
	if err := os.Rename(replacement, executablePath); err != nil {
		t.Fatal(err)
	}

	second, err := resolver.loadElfCaches(processID)
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("replaced executable reused the old ELF cache")
	}
	if _, ok := resolver.procmaps[processID]; ok {
		t.Fatal("replaced executable retained the old process mappings")
	}
}

func TestUsymResolverUsesCachedExecutableAfterExit(t *testing.T) {
	setTestXfsMounts(t, []string{"/"})
	tmpRoot := setupTempProcRoot(t)
	const processID = uint32(1001)
	procDir := filepath.Join(tmpRoot, "proc", strconv.Itoa(int(processID)))
	rootTarget := filepath.Join(tmpRoot, "container-root")
	executablePath := filepath.Join(rootTarget, "usr", "bin", "huatuo-dev")

	mustMkdirAll(t, procDir)
	mustMkdirAll(t, filepath.Dir(executablePath))
	mustSymlink(t, rootTarget, filepath.Join(procDir, "root"))
	mustSymlink(t, "/usr/bin/huatuo-dev", filepath.Join(procDir, "exe"))
	copyCurrentExecutable(t, executablePath)

	name, address := firstFunctionSymbol(t, executablePath)
	resolver := NewUsymResolver()
	if got := resolver.UsymStackStrs(processID, []uint64{address}, 1); len(got) != 1 || got[0] != name {
		t.Fatalf("initial symbols = %v, want [%s]", got, name)
	}
	if err := os.Remove(executablePath); err != nil {
		t.Fatal(err)
	}

	got := resolver.UsymStackStrs(processID, []uint64{address}, 1)
	if len(got) != 1 || got[0] != name {
		t.Fatalf("cached symbols after exit = %v, want [%s]", got, name)
	}
}

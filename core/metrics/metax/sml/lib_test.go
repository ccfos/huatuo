// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The MetaX Authors
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

package sml

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ccfos/huatuo/core/metrics/metax/dl"
	"github.com/ebitengine/purego"
)

func TestLibraryLoadReturnsErrorForIncompleteSMLLibrary(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("shared-library test requires Linux")
	}
	compiler, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang is required to build the test shared library")
	}

	dir := t.TempDir()
	source := filepath.Join(dir, "incomplete.c")
	libraryPath := filepath.Join(dir, "libmxsml.so")
	if err := os.WriteFile(source, []byte("int unrelated_symbol(void) { return 0; }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(compiler, "-shared", "-fPIC", "-o", libraryPath, source).CombinedOutput(); err != nil {
		t.Fatalf("compile incomplete shared library: %v: %s", err, output)
	}

	library := &library{dl: dl.New(libraryPath, purego.RTLD_NOW|purego.RTLD_GLOBAL)}
	err = library.load()
	if err == nil {
		t.Fatal("load() error = nil, want missing required-symbol error")
	}
	if !strings.Contains(err.Error(), "mxSmlInit") {
		t.Fatalf("load() error = %v, want missing mxSmlInit", err)
	}
	if library.dl.Handle() != 0 {
		t.Fatal("library handle remains open after symbol-registration failure")
	}
	if library.refcount != 0 {
		t.Fatalf("reference count = %d, want 0", library.refcount)
	}
}

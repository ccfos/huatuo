// Copyright 2026 The HuaTuo Authors
// Copyright 2026 The Ascend Authors
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

package dcmi

import (
	"strings"
	"testing"

	"github.com/ccfos/huatuo/core/metrics/ascend/dl"
	"github.com/ebitengine/purego"
)

// TestLoadMissingSymbolsReturnsErrorAndCloses verifies that a library which
// loads successfully but lacks the hard-coded DCMI symbols makes load() return
// an error and close the handle, instead of panicking via RegisterLibFunc.
func TestLoadMissingSymbolsReturnsErrorAndCloses(t *testing.T) {
	// libc.so.6 is present on every glibc Linux host but exports none of the
	// hard-coded DCMI symbols, faithfully reproducing the "library exists but
	// its symbol set changed after a CANN/driver upgrade" trigger.
	d := dl.New("libc.so.6", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	lib := &library{dl: d}

	err := lib.load()
	if err == nil {
		t.Fatal("load() returned nil for a library missing all DCMI symbols")
	}
	if !strings.Contains(err.Error(), "dcmi_init") {
		t.Fatalf("load() error = %v, want a missing-symbol error mentioning dcmi_init", err)
	}
	if d.Handle() != 0 {
		t.Fatal("load() did not close the handle after symbol registration failed")
	}
}

// TestLoadMissingLibraryReturnsError pins the graceful degradation the fix
// must preserve: a missing library is an error, not a panic.
func TestLoadMissingLibraryReturnsError(t *testing.T) {
	lib := &library{dl: dl.New("/nonexistent/libdcmi.so", purego.RTLD_NOW|purego.RTLD_GLOBAL)}
	if err := lib.load(); err == nil {
		t.Fatal("load() returned nil for a missing library")
	}
}

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

package testutils

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// fatalTB records a fatal outcome instead of stopping the test binary.
// Fatal/Fatalf emulate testing.TB by unwinding the calling goroutine, so
// helpers that use them can be exercised from a child goroutine.
type fatalTB struct {
	testing.TB

	fatal string
}

func (f *fatalTB) Helper() {}

func (f *fatalTB) Fatalf(format string, args ...any) {
	f.fatal = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func (f *fatalTB) Fatal(args ...any) {
	f.fatal = fmt.Sprint(args...)
	runtime.Goexit()
}

// runCapture executes fn on a child goroutine and waits for it to return,
// fatally exit, or skip.
func runCapture(fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	<-done
}

func TestNativeFileExpandsEndianPlaceholder(t *testing.T) {
	tb := &fatalTB{TB: t}

	var got string
	runCapture(func() {
		got = NativeFile(tb, "../../integration/testdata/fixture-%s.elf")
	})
	if tb.fatal != "" {
		t.Fatalf("NativeFile fatally failed: %s", tb.fatal)
	}

	if !strings.Contains(got, "-el.") && !strings.Contains(got, "-eb.") {
		t.Fatalf("NativeFile() = %q, want an endianness suffix", got)
	}
}

func TestNativeFileRequiresPlaceholder(t *testing.T) {
	tb := &fatalTB{TB: t}
	runCapture(func() {
		NativeFile(tb, "../../integration/testdata/fixture.elf")
	})
	if tb.fatal == "" {
		t.Fatal("expected a failure for a path without the endianness placeholder")
	}
}

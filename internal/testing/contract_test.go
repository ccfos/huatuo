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
	"testing"
)

// recordTB captures fatal and skip outcomes instead of stopping the test
// binary. Fatal/Skip emulate testing.TB by unwinding the calling goroutine,
// so helpers that use them can be exercised from a child goroutine.
type recordTB struct {
	testing.TB

	fatal string
	skip  string
	logs  []string
}

func (r *recordTB) Helper() {}

func (r *recordTB) Logf(format string, args ...any) {
	r.logs = append(r.logs, fmt.Sprintf(format, args...))
}

func (r *recordTB) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func (r *recordTB) Fatal(args ...any) {
	r.fatal = fmt.Sprint(args...)
	runtime.Goexit()
}

func (r *recordTB) Skipf(format string, args ...any) {
	r.skip = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

func (r *recordTB) Skip(args ...any) {
	r.skip = fmt.Sprint(args...)
	runtime.Goexit()
}

// runHelper executes fn on a child goroutine and waits for it to return,
// fatally exit, or skip.
func runHelper(fn func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	<-done
}

func TestMustParseKernelVersion(t *testing.T) {
	tests := []struct {
		in         string
		major, min int
	}{
		{in: "6.6", major: 6, min: 6},
		{in: "4.18", major: 4, min: 18},
		{in: "10.0", major: 10, min: 0},
	}

	for _, tt := range tests {
		tb := &recordTB{TB: t}
		runHelper(func() {
			major, minor := mustParseKernelVersion(tb, tt.in)
			if tb.fatal != "" {
				return
			}
			if major != tt.major || minor != tt.min {
				t.Errorf("mustParseKernelVersion(%q) = (%d, %d), want (%d, %d)",
					tt.in, major, minor, tt.major, tt.min)
			}
		})
		if tb.fatal != "" {
			t.Errorf("mustParseKernelVersion(%q) unexpectedly fatal: %s", tt.in, tb.fatal)
		}
	}
}

func TestMustParseKernelVersionRejectsMalformed(t *testing.T) {
	// The documented input is "major.minor"; anything else must fail loudly
	// rather than silently comparing against a zero version.
	for _, in := range []string{"6", "6.", "6.x", "a.b", "", ".6"} {
		tb := &recordTB{TB: t}
		runHelper(func() {
			mustParseKernelVersion(tb, in)
		})
		if tb.fatal == "" {
			t.Errorf("mustParseKernelVersion(%q) did not fail", in)
		}
	}
}

func TestSkipOnOldKernelNonLinuxIsNoop(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Skip("non-Linux behavior")
	}

	// Non-Linux platforms must neither fatal nor skip on a malformed bound,
	// because the version constraint does not apply there.
	tb := &recordTB{TB: t}
	runHelper(func() {
		SkipOnOldKernel(tb, "not-a-version", "test feature")
	})
	if tb.fatal != "" {
		t.Fatalf("non-Linux SkipOnOldKernel fatally failed: %s", tb.fatal)
	}
	if tb.skip != "" {
		t.Fatalf("non-Linux SkipOnOldKernel skipped: %s", tb.skip)
	}
	if len(tb.logs) == 0 {
		t.Fatal("non-Linux SkipOnOldKernel did not log the ignored constraint")
	}
}

func TestSkipOnOldKernelRespectsRunningVersion(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only behavior")
	}

	major, minor := KernelVersion()
	if major == 0 && minor == 0 {
		t.Skip("cannot determine the running kernel version")
	}

	// A bound above any plausible kernel must skip; the running-version
	// comparison itself is what is under test.
	tb := &recordTB{TB: t}
	runHelper(func() {
		SkipOnOldKernel(tb, "9999.0", "test feature")
	})
	if tb.skip == "" {
		t.Fatalf("SkipOnOldKernel(9999.0) did not skip (running kernel %d.%d)", major, minor)
	}
}

func TestKernelVersionReportsRunningKernel(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux-only behavior")
	}

	major, minor := KernelVersion()
	if major == 0 && minor == 0 {
		t.Skip("cannot determine the running kernel version")
	}

	// huatuo supports Linux 4.18+, so the parser must at least report the
	// running major version rather than the zero value.
	if major < 4 {
		t.Fatalf("KernelVersion() = (%d, %d), want major >= 4", major, minor)
	}
}

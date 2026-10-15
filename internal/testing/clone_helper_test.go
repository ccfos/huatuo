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

func TestAssertDeepCloneDetectsSharedStorage(t *testing.T) {
	type inner struct{ Values []int }
	type outer struct {
		P     *inner
		Items map[string]*inner
	}

	original := &outer{
		P:     &inner{Values: []int{1}},
		Items: map[string]*inner{"a": {Values: []int{2}}},
	}

	clone := &outer{
		P:     &inner{Values: []int{1}},
		Items: map[string]*inner{"a": {Values: []int{2}}},
	}

	tb := &fatalTB{TB: t}
	runCapture(func() {
		AssertDeepClone(tb, original, clone)
	})
	if tb.fatal != "" {
		t.Fatalf("independent clone reported as shared: %s", tb.fatal)
	}

	shallow := &outer{P: original.P, Items: original.Items}
	runCapture(func() {
		AssertDeepClone(tb, original, shallow)
	})
	if tb.fatal == "" {
		t.Fatal("AssertDeepClone accepted a shallow copy sharing pointers and maps")
	}
}

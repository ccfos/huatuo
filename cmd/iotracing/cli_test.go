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

package main

import (
	"testing"
	"time"
)

// TestValidateDurationRejectsValuesThatOverflow covers the bound: trace.go
// turns --duration into a time.Duration with a seconds multiply, so a value
// above MaxInt64/time.Second wraps negative and the trace window collapses to
// an already-expired context instead of running.
func TestValidateDurationRejectsValuesThatOverflow(t *testing.T) {
	if err := validateDuration(8); err != nil {
		t.Fatalf("validateDuration(8) error = %v, want nil", err)
	}
	if err := validateDuration(maxDurationSeconds); err != nil {
		t.Fatalf("validateDuration(%d) error = %v, want nil", maxDurationSeconds, err)
	}

	for _, seconds := range []uint64{
		0,
		maxDurationSeconds + 1,
		9223372037, // MaxInt64/time.Second + 1
		^uint64(0), // MaxUint64
	} {
		if err := validateDuration(seconds); err == nil {
			t.Errorf("validateDuration(%d) error = nil, want rejection", seconds)
		}
	}
}

// TestMaxDurationSecondsDoesNotOverflow keeps the bound and the conversion in
// trace.go in agreement: the largest accepted value still converts to a
// positive duration, the next one does not.
func TestMaxDurationSecondsDoesNotOverflow(t *testing.T) {
	if got := time.Duration(maxDurationSeconds) * time.Second; got <= 0 {
		t.Fatalf("maxDurationSeconds converts to %v, want a positive duration", got)
	}
	// A variable keeps the multiply from being folded at compile time, where
	// the overflow would be rejected outright.
	overflowing := maxDurationSeconds + 1
	if got := time.Duration(overflowing) * time.Second; got > 0 {
		t.Fatalf("maxDurationSeconds+1 converts to %v, want the overflow this bound prevents", got)
	}
}

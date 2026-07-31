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

package profiling

import "testing"

func TestParseLockMode(t *testing.T) {
	for _, mode := range []LockMode{LockModeWaitTime, LockModeCount} {
		got, err := ParseLockMode(string(mode))
		if err != nil {
			t.Fatalf("ParseLockMode(%q) error = %v", mode, err)
		}
		if got != mode {
			t.Errorf("ParseLockMode(%q) = %q", mode, got)
		}
	}
	if _, err := ParseLockMode("latency"); err == nil {
		t.Fatal("ParseLockMode(latency) error = nil")
	}
}

func TestParseLockType(t *testing.T) {
	for _, lockType := range []LockType{
		LockTypeMutex,
		LockTypeSpinlock,
		LockTypeRWLock,
	} {
		got, err := ParseLockType(string(lockType))
		if err != nil {
			t.Fatalf("ParseLockType(%q) error = %v", lockType, err)
		}
		if got != lockType {
			t.Errorf("ParseLockType(%q) = %q", lockType, got)
		}
	}
	if _, err := ParseLockType("semaphore"); err == nil {
		t.Fatal("ParseLockType(semaphore) error = nil")
	}
}

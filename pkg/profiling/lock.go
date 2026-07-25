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

import "fmt"

// LockMode identifies the value represented by a lock profile.
type LockMode string

const (
	LockModeUnknown  LockMode = ""
	LockModeWaitTime LockMode = "wait_time"
)

// LockType identifies the kernel lock primitive being profiled.
type LockType string

const (
	LockTypeUnknown LockType = ""
	LockTypeMutex   LockType = "mutex"
	LockTypeRWLock  LockType = "rwlock"
)

// ParseLockType returns a supported native kernel lock primitive.
func ParseLockType(value string) (LockType, error) {
	lockType := LockType(value)
	switch lockType {
	case LockTypeMutex, LockTypeRWLock:
		return lockType, nil
	default:
		return LockTypeUnknown, fmt.Errorf(
			"unsupported lock type %q (expected: mutex or rwlock)",
			value,
		)
	}
}

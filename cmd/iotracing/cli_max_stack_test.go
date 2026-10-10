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

import "testing"

// TestValidateMaxStackRejectsUnallocatableValues covers the bound: --max-stack
// sizes make([]types.IOScheduleEvent, maxStack), so a value whose byte size
// overflows the allocation makes runtime.makeslice panic instead of reporting.
func TestValidateMaxStackRejectsUnallocatableValues(t *testing.T) {
	for _, maxStack := range []uint64{0, 1, 10, maxStackLimit} {
		if err := validateMaxStack(maxStack); err != nil {
			t.Errorf("validateMaxStack(%d) error = %v, want nil", maxStack, err)
		}
	}

	for _, maxStack := range []uint64{
		maxStackLimit + 1,
		100000000000, // ~8 TB of IOScheduleEvent
		9223372036854775807,
		^uint64(0),
	} {
		if err := validateMaxStack(maxStack); err == nil {
			t.Errorf("validateMaxStack(%d) error = nil, want rejection", maxStack)
		}
	}
}

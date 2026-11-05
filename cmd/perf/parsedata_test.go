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
	"fmt"
	"testing"
)

func TestNameIndexFindOrAdd(t *testing.T) {
	names := newNameIndex()
	if got := names.findOrAdd("main"); got != 0 {
		t.Fatalf("first index = %d, want 0", got)
	}
	if got := names.findOrAdd("main"); got != 0 {
		t.Fatalf("duplicate index = %d, want 0", got)
	}
	if got := names.findOrAdd("read"); got != 1 {
		t.Fatalf("second name index = %d, want 1", got)
	}
	if len(names.names) != 2 || names.names[0] != "main" || names.names[1] != "read" {
		t.Fatalf("names = %v", names.names)
	}
}

func BenchmarkNameIndexFindOrAddExisting(b *testing.B) {
	names := newNameIndex()
	for i := range 10000 {
		names.findOrAdd(fmt.Sprintf("func_%04d", i))
	}
	target := "func_0999"

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		names.findOrAdd(target)
	}
}

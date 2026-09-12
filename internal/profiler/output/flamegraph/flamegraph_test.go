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

package flamegraph

import (
	"testing"

	"huatuo-bamai/internal/profiler/output"
)

func TestFormatterToStacksTrimsFramesAndSortsBySamples(t *testing.T) {
	f := New()
	if err := f.Add(&output.Sample{Frames: []string{" root ", "leaf"}, Count: 2}); err != nil {
		t.Fatalf("Add first stack: %v", err)
	}
	if err := f.Add(&output.Sample{Frames: []string{"other"}, Count: 5}); err != nil {
		t.Fatalf("Add second stack: %v", err)
	}

	stacks := f.toStacks()
	if len(stacks) != 2 {
		t.Fatalf("toStacks() returned %d stacks, want 2", len(stacks))
	}
	if stacks[0].Samples != 5 || stacks[0].Names[0] != "other" {
		t.Fatalf("first stack = %#v, want highest-count other stack", stacks[0])
	}
	if stacks[1].Samples != 2 || len(stacks[1].Names) != 2 || stacks[1].Names[0] != "root" {
		t.Fatalf("second stack = %#v, want trimmed root/leaf stack", stacks[1])
	}
}

func TestFormatterName(t *testing.T) {
	if got := New().Name(); got != "flamegraph" {
		t.Errorf("Name() = %q, want flamegraph", got)
	}
}

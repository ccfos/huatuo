// Copyright 2025 The HuaTuo Authors
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

package memray

import "testing"

func TestGetTraceIndexBinarySearchDedup(t *testing.T) {
	var ft frameTree
	// Insert several children under the root out of order; the tree must keep
	// them sorted by FrameID and deduplicate repeated inserts.
	a := ft.getTraceIndex(0, 30)
	b := ft.getTraceIndex(0, 10)
	c := ft.getTraceIndex(0, 20)
	if a == b || b == c || a == c {
		t.Fatalf("expected distinct indices, got %d %d %d", a, b, c)
	}
	// Re-inserting the same FrameID under the same parent must return the
	// existing child index.
	if got := ft.getTraceIndex(0, 20); got != c {
		t.Fatalf("expected dedup index %d, got %d", c, got)
	}
	if got := ft.getTraceIndex(0, 30); got != a {
		t.Fatalf("expected dedup index %d, got %d", a, got)
	}

	// Children must remain sorted by FrameID for the binary search to work.
	children := ft.nodes[0].Children
	for i := 1; i < len(children); i++ {
		if children[i-1].FrameID >= children[i].FrameID {
			t.Fatalf("children not strictly sorted at %d", i)
		}
	}
}

func TestRegisterPyFrameLineLevelIdentity(t *testing.T) {
	rd := &reader{
		pyFrameIndex: make(map[pyFrameKey]uint64),
		pyFrames:     make([]pyFrameKey, 0, 8),
	}
	// The same code object executing different instruction offsets (i.e.
	// different source lines) must produce distinct frame ids; identical keys
	// must deduplicate.
	sameFuncLine1 := rd.registerPyFrame(pyFrameKey{CodeObjectID: 1, InstrOffset: 0, IsEntry: true})
	sameFuncLine2 := rd.registerPyFrame(pyFrameKey{CodeObjectID: 1, InstrOffset: 8, IsEntry: true})
	if sameFuncLine1 == sameFuncLine2 {
		t.Fatalf("expected distinct frame ids for different instruction offsets")
	}
	if got := rd.registerPyFrame(pyFrameKey{CodeObjectID: 1, InstrOffset: 0, IsEntry: true}); got != sameFuncLine1 {
		t.Fatalf("expected identical key to dedup to %d, got %d", sameFuncLine1, got)
	}
	// A different isEntry flag is also a distinct frame.
	if got := rd.registerPyFrame(pyFrameKey{CodeObjectID: 1, InstrOffset: 0, IsEntry: false}); got == sameFuncLine1 {
		t.Fatalf("expected distinct frame id for non-entry frame")
	}
}

func TestPythonFrameLabel(t *testing.T) {
	rd := &reader{
		codeObjects: map[uint64]codeObject{
			1: {Func: "do_work", Filename: "app.py", FirstLineNo: 10, Linetable: []byte{}},
			2: {Func: "helper", Filename: "util.py"}, // no linetable
			3: {Func: "", Linetable: []byte{}},       // missing metadata
		},
	}
	cases := map[uint64]string{
		1: "do_work (app.py)",
		2: "helper (util.py)",
		3: "[unknown]",
	}
	for codeID, want := range cases {
		if got := rd.pythonFrameLabel(codeID, 0); got != want {
			t.Fatalf("codeID=%d: expected %q got %q", codeID, want, got)
		}
	}
}

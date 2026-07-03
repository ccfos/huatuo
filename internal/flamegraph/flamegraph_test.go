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
)

func TestLevelsToTreeBasic(t *testing.T) {
	// A simple two-level flamebearer:
	// Level 0: root bar [0, 100, 0, 0] → name index 0, value 100, self 0
	// Level 1: child bar [0, 100, 50, 1] → name index 1, value 100, self 50
	levels := []*Level{
		{Values: []int64{0, 100, 0, 0}},
		{Values: []int64{0, 100, 50, 1}},
	}
	names := []string{"root", "child"}

	tree := LevelsToTree(levels, names)
	if tree == nil {
		t.Fatal("LevelsToTree() = nil, want non-nil")
	}
	if tree.Name != "root" {
		t.Errorf("root.Name = %q, want %q", tree.Name, "root")
	}
	if tree.Value != 100 {
		t.Errorf("root.Value = %d, want 100", tree.Value)
	}
	if tree.Start != 0 {
		t.Errorf("root.Start = %d, want 0", tree.Start)
	}
	if tree.Level != 0 {
		t.Errorf("root.Level = %d, want 0", tree.Level)
	}
	if len(tree.Nodes) != 1 {
		t.Fatalf("len(tree.Nodes) = %d, want 1", len(tree.Nodes))
	}
	child := tree.Nodes[0]
	if child.Name != "child" {
		t.Errorf("child.Name = %q, want %q", child.Name, "child")
	}
	if child.Value != 100 {
		t.Errorf("child.Value = %d, want 100", child.Value)
	}
	if child.Self != 50 {
		t.Errorf("child.Self = %d, want 50", child.Self)
	}
	if child.Level != 1 {
		t.Errorf("child.Level = %d, want 1", child.Level)
	}
}

func TestLevelsToTreeEmpty(t *testing.T) {
	tree := LevelsToTree(nil, nil)
	if tree != nil {
		t.Errorf("LevelsToTree(nil, nil) = %v, want nil", tree)
	}

	tree = LevelsToTree([]*Level{}, []string{})
	if tree != nil {
		t.Errorf("LevelsToTree(empty) = %v, want nil", tree)
	}
}

func TestLevelsToTreeMultiLevel(t *testing.T) {
	// Three-level flamebearer:
	// Level 0: root [0, 100, 0, 0]
	// Level 1: child-0 [0, 60, 10, 1], child-1 [60, 40, 5, 2]
	// Level 2: grandchild-0 [0, 30, 15, 3] (under child-0)
	levels := []*Level{
		{Values: []int64{0, 100, 0, 0}},
		{Values: []int64{0, 60, 10, 1, 60, 40, 5, 2}},
		{Values: []int64{0, 30, 15, 3}},
	}
	names := []string{"root", "child-0", "child-1", "grandchild-0"}

	tree := LevelsToTree(levels, names)
	if tree == nil {
		t.Fatal("LevelsToTree() = nil, want non-nil")
	}
	if len(tree.Nodes) != 2 {
		t.Fatalf("len(root.Nodes) = %d, want 2", len(tree.Nodes))
	}

	child0 := tree.Nodes[0]
	if child0.Name != "child-0" {
		t.Errorf("child0.Name = %q, want %q", child0.Name, "child-0")
	}
	if len(child0.Nodes) != 1 {
		t.Fatalf("len(child0.Nodes) = %d, want 1", len(child0.Nodes))
	}

	grandchild := child0.Nodes[0]
	if grandchild.Name != "grandchild-0" {
		t.Errorf("grandchild.Name = %q, want %q", grandchild.Name, "grandchild-0")
	}
	if grandchild.Self != 15 {
		t.Errorf("grandchild.Self = %d, want 15", grandchild.Self)
	}
}

func TestTreeToNestedSetDataFrame(t *testing.T) {
	tree := &ProfileTree{
		Start: 0,
		Value: 100,
		Self:  20,
		Level: 0,
		Name:  "root",
		Nodes: []*ProfileTree{
			{Start: 0, Value: 80, Self: 30, Level: 1, Name: "child"},
		},
	}

	frame, labelField := TreeToNestedSetDataFrame(tree, "bytes")
	if frame == nil {
		t.Fatal("TreeToNestedSetDataFrame() returned nil frame")
	}
	if labelField == nil {
		t.Fatal("TreeToNestedSetDataFrame() returned nil labelField")
	}

	// frame should have 4 fields: level, value, self, label
	if len(frame.Fields) != 4 {
		t.Fatalf("len(frame.Fields) = %d, want 4", len(frame.Fields))
	}

	// Each field should have 2 rows (root + child)
	for i, f := range frame.Fields {
		if f.Len() != 2 {
			t.Errorf("field[%d] (%s).Len() = %d, want 2", i, f.Name, f.Len())
		}
	}
}

func TestTreeToNestedSetDataFrameNilTree(t *testing.T) {
	frame, _ := TreeToNestedSetDataFrame(nil, "bytes")
	if frame == nil {
		t.Fatal("TreeToNestedSetDataFrame(nil) = nil, want non-nil frame")
	}
	if len(frame.Fields) < 3 {
		t.Errorf("len(frame.Fields) = %d, want at least 3", len(frame.Fields))
	}
}

func TestEnumField(t *testing.T) {
	ef := NewEnumField("test", nil)
	if ef == nil {
		t.Fatal("NewEnumField() = nil")
	}

	ef.Append("alpha")
	ef.Append("beta")
	ef.Append("alpha") // duplicate, should reuse index

	m := ef.GetValuesMap()
	if len(m) != 2 {
		t.Errorf("len(valuesMap) = %d, want 2", len(m))
	}

	f := ef.GetField()
	if f.Len() != 3 {
		t.Errorf("field.Len() = %d, want 3", f.Len())
	}
}

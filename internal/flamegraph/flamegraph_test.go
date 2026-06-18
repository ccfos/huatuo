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

import "testing"

func TestLevelsToTree(t *testing.T) {
	levels := []*Level{
		{Values: []int64{0, 100, 10, 0}},
		{Values: []int64{0, 60, 5, 1, 0, 40, 7, 2}},
	}
	names := []string{"root", "child-a", "child-b"}

	tree := LevelsToTree(levels, names)
	if tree == nil {
		t.Fatalf("LevelsToTree() = nil, want tree")
	}
	if tree.Name != "root" || tree.Value != 100 || tree.Self != 10 {
		t.Fatalf("root = %+v, want root value=100 self=10", tree)
	}
	if len(tree.Nodes) != 2 {
		t.Fatalf("root children = %d, want 2", len(tree.Nodes))
	}
	if tree.Nodes[0].Name != "child-a" || tree.Nodes[0].Start != 0 || tree.Nodes[0].Value != 60 {
		t.Fatalf("first child = %+v, want child-a start=0 value=60", tree.Nodes[0])
	}
	if tree.Nodes[1].Name != "child-b" || tree.Nodes[1].Start != 60 || tree.Nodes[1].Value != 40 {
		t.Fatalf("second child = %+v, want child-b start=60 value=40", tree.Nodes[1])
	}
}

func TestLevelsToTreeEmpty(t *testing.T) {
	if tree := LevelsToTree(nil, nil); tree != nil {
		t.Fatalf("LevelsToTree(nil, nil) = %+v, want nil", tree)
	}
}

func TestTreeToNestedSetDataFrame(t *testing.T) {
	tree := &ProfileTree{
		Value: 100,
		Self:  10,
		Name:  "root",
		Nodes: []*ProfileTree{
			{Level: 1, Value: 60, Self: 5, Name: "child"},
		},
	}

	frame, labels := TreeToNestedSetDataFrame(tree, "samples")
	if frame == nil {
		t.Fatalf("TreeToNestedSetDataFrame() frame is nil")
	}
	if len(frame.Fields) != 4 {
		t.Fatalf("fields = %d, want 4", len(frame.Fields))
	}
	if frame.Fields[0].Len() != 2 {
		t.Fatalf("rows = %d, want 2", frame.Fields[0].Len())
	}
	if len(labels.GetValuesMap()) != 2 {
		t.Fatalf("labels = %v, want root and child", labels.GetValuesMap())
	}
}

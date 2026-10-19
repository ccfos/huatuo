// Copyright 2026
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package flamegraph

import "testing"

// TestProcessDistinctParentsSameChildName guards the per-parent frame
// index: two parents sharing a child name must produce two separate
// child frames, not merge samples into whichever frame was indexed
// first.
func TestProcessDistinctParentsSameChildName(t *testing.T) {
	var proc processor
	proc.Process(Stack{Names: []string{"A", "X"}, Samples: 1})
	proc.Process(Stack{Names: []string{"B", "X"}, Samples: 2})

	byName := map[string]*frame{}
	for i := range proc.frames {
		f := &proc.frames[i]
		if f.Depth == 0 {
			byName[f.Name] = f
		}
	}

	a, b := byName["A"], byName["B"]
	if a == nil || b == nil {
		t.Fatalf("expected distinct root frames A and B, got %+v", proc.frames)
	}
	if len(a.Children) != 1 || a.Children[0].Name != "X" || a.Children[0].SampleCount != 1 {
		t.Fatalf("A child wrong: %+v", a.Children)
	}
	if len(b.Children) != 1 || b.Children[0].Name != "X" || b.Children[0].SampleCount != 2 {
		t.Fatalf("B child wrong: %+v", b.Children)
	}
}

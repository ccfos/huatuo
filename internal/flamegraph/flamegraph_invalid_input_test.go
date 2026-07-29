// Copyright 2026 The HuaTuo Authors
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

func TestLevelsToTreeRejectsMalformedRoot(t *testing.T) {
	tests := []struct {
		name   string
		levels []*Level
		names  []string
	}{
		{
			name:   "truncated root item",
			levels: []*Level{{Values: []int64{0, 10, 1}}},
			names:  []string{"root"},
		},
		{
			name:   "invalid root name index",
			levels: []*Level{{Values: []int64{0, 10, 1, 1}}},
			names:  []string{"root"},
		},
		{
			name:   "nil root level",
			levels: []*Level{nil},
			names:  []string{"root"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LevelsToTree(tt.levels, tt.names); got != nil {
				t.Fatalf("LevelsToTree() = %#v, want nil", got)
			}
		})
	}
}

func TestLevelsToTreeSkipsMalformedChildLevel(t *testing.T) {
	levels := []*Level{
		{Values: []int64{0, 10, 1, 0}},
		{Values: []int64{0, 5, 1}},
	}

	tree := LevelsToTree(levels, []string{"root"})
	if tree == nil {
		t.Fatal("LevelsToTree() = nil, want root tree")
	}
	if len(tree.Nodes) != 0 {
		t.Fatalf("root child count = %d, want 0", len(tree.Nodes))
	}
}

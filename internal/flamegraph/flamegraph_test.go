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

func TestLevelsToTreeUsesRootNameIndex(t *testing.T) {
	levels := []*Level{
		{Values: []int64{0, 10, 1, 2}},
	}
	names := []string{"offset-zero", "other", "root"}

	tree := LevelsToTree(levels, names)
	if tree == nil {
		t.Fatalf("LevelsToTree returned nil")
	}
	if tree.Name != "root" {
		t.Fatalf("root name = %q, want %q", tree.Name, "root")
	}
}

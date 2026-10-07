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

package profiler

// TreeItem is the item in the tree.
type TreeItem struct {
	Stack [][]byte `json:"stack,omitempty"`
	Value uint64   `json:"value,omitempty"`
}

// BuildTreeItem joins optional prefixes with outermost-first user and kernel
// frames in the order expected by ParseTree.
func BuildTreeItem(prefixes, userFrames, kernelFrames []string, value uint64) *TreeItem {
	stack := make([][]byte, 0, len(prefixes)+len(userFrames)+len(kernelFrames))

	for _, frame := range prefixes {
		stack = append(stack, []byte(frame))
	}
	for _, frame := range userFrames {
		stack = append(stack, []byte(frame))
	}
	for _, frame := range kernelFrames {
		stack = append(stack, []byte(frame))
	}

	return &TreeItem{Stack: stack, Value: value}
}

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

package tui

import "github.com/ccfos/huatuo/internal/profiling/flamegraph"

// NewModelFromFrameData prepares the viewer from ordered flamegraph frames.
func NewModelFromFrameData(data []flamegraph.FrameData) *Model {
	model := newModel(BuildTree(data))
	return &model
}

// BuildTree reconstructs parent/child relationships from ordered FrameData.
func BuildTree(data []flamegraph.FrameData) *Node {
	if len(data) == 0 {
		return &Node{FrameData: flamegraph.FrameData{Label: "empty", Value: 0}}
	}

	var roots []*Node
	stack := make([]*Node, 0, 16)
	for _, frame := range data {
		node := &Node{FrameData: frame}
		level := int(frame.Level)
		if level < 0 {
			level = 0
		}

		if level == 0 || len(stack) == 0 {
			roots = append(roots, node)
			stack = []*Node{node}
			continue
		}

		if level > len(stack) {
			level = len(stack)
		}
		parent := stack[level-1]
		node.Parent = parent
		parent.Children = append(parent.Children, node)

		if level < len(stack) {
			stack = stack[:level]
		}
		stack = append(stack, node)
	}

	if len(roots) == 1 {
		return roots[0]
	}

	root := &Node{
		FrameData: flamegraph.FrameData{Label: "all"},
		Children:  roots,
	}
	for _, child := range roots {
		child.Parent = root
		root.Value += child.Value
	}
	return root
}

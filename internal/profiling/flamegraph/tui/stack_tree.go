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

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/ccfos/huatuo/internal/profiling/flamegraph"
)

type childKey struct {
	parent *Node
	name   string
}

// StackTree accumulates structured stacks without changing their frame names.
// Add borrows frame slices only for the duration of the call.
type StackTree struct {
	ctx   context.Context
	root  *Node
	nodes map[childKey]*Node
}

// NewStackTree creates a stack builder whose preparation observes ctx.
func NewStackTree(ctx context.Context) *StackTree {
	return &StackTree{
		ctx:   ctx,
		root:  &Node{FrameData: flamegraph.FrameData{Label: "all"}},
		nodes: make(map[childKey]*Node),
	}
}

// Add inserts root-first frames with a nonnegative weight.
// The caller must discard the builder after an error.
func (t *StackTree) Add(frames []string, weight int64) error {
	if err := t.ctx.Err(); err != nil {
		return err
	}
	if t.root == nil {
		return fmt.Errorf("stack tree has already been built")
	}
	if weight < 0 {
		return fmt.Errorf("stack has negative weight %d", weight)
	}
	if weight == 0 {
		return nil
	}
	if len(frames) == 0 {
		return fmt.Errorf("stack with weight %d has no frames", weight)
	}
	if t.root.Value > math.MaxInt64-weight {
		return fmt.Errorf("stack total weight exceeds int64")
	}

	parent := t.root
	parent.Value += weight
	for _, name := range frames {
		if err := t.ctx.Err(); err != nil {
			return err
		}

		parent = t.findOrCreateChild(parent, name)
		parent.Value += weight
	}
	parent.Self += weight

	return nil
}

func (t *StackTree) findOrCreateChild(parent *Node, name string) *Node {
	key := childKey{
		parent: parent,
		name:   name,
	}
	if node := t.nodes[key]; node != nil {
		return node
	}

	node := &Node{
		FrameData: flamegraph.FrameData{
			Label: key.name,
			Level: key.parent.Level + 1,
		},
		Parent: key.parent,
	}
	parent.Children = append(parent.Children, node)
	t.nodes[key] = node

	return node
}

// BuildModel transfers the tree to a new viewer model. It returns nil when all
// stacks have zero weight. After a model is returned, the builder is consumed.
func (t *StackTree) BuildModel() (*Model, error) {
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}
	if t.root == nil {
		return nil, fmt.Errorf("stack tree has already been built")
	}
	if t.root.Value == 0 {
		return nil, nil
	}

	// Stable sibling order must not depend on sample arrival order.
	nodes := []*Node{t.root}
	for i := 0; i < len(nodes); i++ {
		if err := t.ctx.Err(); err != nil {
			return nil, err
		}

		node := nodes[i]
		sort.Slice(node.Children, func(i, j int) bool {
			return node.Children[i].Label < node.Children[j].Label
		})
		nodes = append(nodes, node.Children...)
		if err := t.ctx.Err(); err != nil {
			return nil, err
		}
	}

	root := t.root
	if len(root.Children) == 1 {
		root = root.Children[0]
	}

	model := newModel(root)
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}
	root.Parent = nil
	t.root = nil
	t.nodes = nil

	return &model, nil
}

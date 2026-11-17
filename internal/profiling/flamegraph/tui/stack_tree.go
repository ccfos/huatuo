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

	phlaremodel "github.com/grafana/pyroscope/pkg/model"

	"github.com/ccfos/huatuo/internal/profiling/flamegraph"
)

// StackTree accumulates structured stacks without changing their frame names.
// Add borrows frame slices only for the duration of the call.
type StackTree struct {
	ctx   context.Context
	tree  *phlaremodel.StacktraceTree
	ids   map[string]int32
	names []string
	total int64
}

// NewStackTree creates a stack builder whose preparation observes ctx.
func NewStackTree(ctx context.Context) *StackTree {
	return &StackTree{
		ctx:  ctx,
		tree: phlaremodel.NewStacktraceTree(0),
		ids:  make(map[string]int32),
	}
}

// Add inserts root-first frames with a nonnegative weight.
// The caller must discard the builder after an error.
func (t *StackTree) Add(frames []string, weight int64) error {
	if err := t.ctx.Err(); err != nil {
		return err
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
	if t.total > math.MaxInt64-weight {
		return fmt.Errorf("stack total weight exceeds int64")
	}

	locations := make([]int32, len(frames))
	for i, name := range frames {
		if err := t.ctx.Err(); err != nil {
			return err
		}

		id, ok := t.ids[name]
		if !ok {
			if len(t.names) > math.MaxInt32 {
				return fmt.Errorf("stacks contain too many function names")
			}
			id = int32(len(t.names))
			t.names = append(t.names, name)
			t.ids[name] = id
		}
		// Pyroscope consumes leaf-first location IDs.
		locations[len(frames)-1-i] = id
	}
	t.tree.Insert(locations, weight)
	t.total += weight
	return nil
}

// BuildModel prepares the viewer, or returns nil when all stacks have zero weight.
func (t *StackTree) BuildModel() (*Model, error) {
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}
	if t.total == 0 {
		return nil, nil
	}

	root := &Node{FrameData: flamegraph.FrameData{
		Label: "all",
		Value: t.total,
	}}
	// Insert creates parents before children; no second tree-building pass is needed.
	nodes := make([]*Node, len(t.tree.Nodes))
	nodes[0] = root
	for i := 1; i < len(t.tree.Nodes); i++ {
		if err := t.ctx.Err(); err != nil {
			return nil, err
		}

		frame := &t.tree.Nodes[i]
		parent := nodes[frame.Parent]
		node := &Node{
			FrameData: flamegraph.FrameData{
				Label: t.names[frame.Location],
				Value: frame.Total,
				Self:  frame.Value,
				Level: parent.Level + 1,
			},
			Parent: parent,
		}
		parent.Children = append(parent.Children, node)
		nodes[i] = node
	}
	// Stable sibling order must not depend on sample arrival order.
	for _, node := range nodes {
		if err := t.ctx.Err(); err != nil {
			return nil, err
		}

		sort.Slice(node.Children, func(i, j int) bool {
			return node.Children[i].Label < node.Children[j].Label
		})
		if err := t.ctx.Err(); err != nil {
			return nil, err
		}
	}
	if len(root.Children) == 1 {
		root = root.Children[0]
		root.Parent = nil
	}

	model := newModel(root, "HUATUO profiler flamegraph")
	if err := t.ctx.Err(); err != nil {
		return nil, err
	}

	return &model, nil
}

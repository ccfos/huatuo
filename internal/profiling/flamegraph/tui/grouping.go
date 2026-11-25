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
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

type groupingState struct {
	ungroupedTree treeNodes
	buildTree     func(context.Context, []string) (*StackTree, error)
	ctx           context.Context
	buildMu       sync.Mutex
	tags          []string
	input         string
	errorText     string
	editing       bool
	pending       bool
}

type groupingResult struct {
	nodes           treeNodes
	functionParents []*Node
	tags            []string
	err             error
}

func (m *Model) updateGrouping(key tea.KeyMsg) tea.Cmd {
	grouping := m.grouping
	grouping.errorText = ""
	switch key.Type {
	case tea.KeyEsc:
		grouping.editing = false
	case tea.KeyCtrlU:
		grouping.input = ""
	case tea.KeyBackspace:
		_, size := utf8.DecodeLastRuneInString(grouping.input)
		grouping.input = grouping.input[:len(grouping.input)-size]
	case tea.KeySpace:
		grouping.input += " "
	case tea.KeyRunes:
		grouping.input += string(key.Runes)
	case tea.KeyEnter:
		return m.submitGrouping()
	}
	return nil
}

func (m *Model) submitGrouping() tea.Cmd {
	grouping := m.grouping
	tags, err := parseGrouping(grouping.input)
	if err != nil {
		grouping.errorText = err.Error()
		return nil
	}

	grouping.editing = false
	if slices.Equal(tags, grouping.tags) {
		return nil
	}
	if len(tags) == 0 {
		m.applyGrouping(&groupingResult{nodes: grouping.ungroupedTree})
		return nil
	}

	grouping.pending = true
	ctx := grouping.ctx
	previous := m.tree
	order := groupingOrder(grouping.tags, tags)
	return func() tea.Msg {
		return grouping.build(ctx, tags, previous, order)
	}
}

func parseGrouping(input string) ([]string, error) {
	if strings.TrimSpace(input) == "" {
		return nil, nil
	}

	var tags []string
	for field := range strings.SplitSeq(input, ",") {
		tag := strings.TrimSpace(field)
		switch tag {
		case "cpu", "comm", "pid":
		default:
			return nil, fmt.Errorf("invalid grouping label %q (use cpu, comm or pid)", tag)
		}
		if slices.Contains(tags, tag) {
			return nil, fmt.Errorf("duplicate grouping label %q", tag)
		}

		tags = append(tags, tag)
	}
	return tags, nil
}

func (g *groupingState) build(
	ctx context.Context, tags []string, previous treeNodes, order []int,
) groupingResult {
	g.buildMu.Lock()
	defer g.buildMu.Unlock()
	// A queued command can start after the viewer has already closed.
	if err := ctx.Err(); err != nil {
		return groupingResult{err: err}
	}

	if order != nil {
		nodes, functionParents, err := reorderGroupLabels(ctx, previous, order)
		if err != nil {
			return groupingResult{err: fmt.Errorf("reorder groups by %s: %w", strings.Join(tags, ","), err)}
		}

		return groupingResult{
			nodes:           nodes,
			functionParents: functionParents,
			tags:            tags,
		}
	}

	tree, err := g.buildTree(ctx, tags)
	if err != nil {
		return groupingResult{err: fmt.Errorf("group by %s: %w", strings.Join(tags, ","), err)}
	}
	nodes, err := tree.buildNodes()
	if err != nil {
		return groupingResult{err: fmt.Errorf("group by %s: %w", strings.Join(tags, ","), err)}
	}
	if len(nodes) == 0 {
		return groupingResult{err: fmt.Errorf("group by %s: no weighted samples", strings.Join(tags, ","))}
	}

	return groupingResult{
		nodes: nodes,
		tags:  tags,
	}
}

func (m *Model) applyGrouping(result *groupingResult) {
	if m.quitWithQ {
		return
	}

	grouping := m.grouping
	grouping.pending = false
	if result.err != nil {
		// Worker errors can include report text. Input errors already quote labels.
		grouping.errorText = terminalText(result.err.Error())
		return
	}

	// The old view remains readable until the event loop installs the new one.
	for _, parent := range result.functionParents {
		for _, child := range parent.Children {
			child.Parent = parent
		}
	}

	grouping.errorText = ""
	grouping.tags = result.tags
	m.tree = result.nodes
	m.focusOn(m.tree.root())
}

func (g *groupingState) status() string {
	if g.errorText != "" {
		return g.errorText
	}
	if g.editing {
		return "group by: " + terminalText(g.input) + "_ (cpu,comm,pid | empty clears)"
	}
	if g.pending {
		return "grouping stacks…"
	}

	current := strings.Join(g.tags, ",")
	if len(g.tags) == 0 {
		current = "none"
	}
	return "groups: " + current + " (g edit)"
}

// groupingOrder maps each requested tag to its previous position. A nil order
// means the grouping fields changed and the samples must be grouped again.
func groupingOrder(previous, tags []string) []int {
	if len(previous) == 0 || len(previous) != len(tags) {
		return nil
	}

	order := make([]int, len(tags))
	for index, tag := range tags {
		order[index] = slices.Index(previous, tag)
		if order[index] < 0 {
			return nil
		}
	}

	return order
}

// reorderGroupLabels borrows the existing function subtrees while the labels
// move. Their parent links are updated only when the event loop adopts the view.
func reorderGroupLabels(ctx context.Context, previous treeNodes, order []int) (treeNodes, []*Node, error) {
	const cancelCheckInterval = 4096

	type functionSubtree struct {
		children []*Node
		frames   []*Node
	}

	labelTree := NewStackTree(ctx)
	labelDepth := int64(len(order))
	labelPath := make([]string, len(order))
	subtrees := make(map[*Node]functionSubtree)
	var functionCount int

	for next := 0; next < len(previous); {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		label := previous[next]
		next++
		if label.Level == 0 {
			continue // The synthetic root is outside the label path.
		}
		labelPath[label.Level-1] = label.Label
		if label.Level != labelDepth {
			continue
		}

		// Preorder keeps the function frames contiguous after the deepest label.
		frameStart := next
		for next < len(previous) && previous[next].Level > labelDepth {
			if next%cancelCheckInterval == 0 {
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
			}
			next++
		}
		frames := previous[frameStart:next]

		newLeaf := labelTree.root
		newLeaf.Value += label.Value
		for _, index := range order {
			newLeaf = labelTree.findOrCreateChild(newLeaf, labelPath[index])
			newLeaf.Value += label.Value
		}
		subtrees[newLeaf] = functionSubtree{
			children: label.Children,
			frames:   frames,
		}
		functionCount += len(frames)
	}

	// Sort the new labels before attaching their shared function subtrees.
	labels, err := labelTree.buildNodes()
	if err != nil {
		return nil, nil, err
	}

	nodes := make(treeNodes, 0, len(labels)+functionCount)
	functionParents := make([]*Node, 0, len(subtrees))
	for _, label := range labels {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}

		nodes = append(nodes, label)
		if label.Level != labelDepth {
			continue
		}

		subtree := subtrees[label]
		label.Children = subtree.children
		nodes = append(nodes, subtree.frames...)
		functionParents = append(functionParents, label)
	}
	return nodes, functionParents, nil
}

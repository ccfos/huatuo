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
	ungroupedTree   treeNodes
	allowedLabels   []string
	buildTree       func(context.Context, []string) (*StackTree, error)
	ctx             context.Context
	buildMu         sync.Mutex
	groupLabelNames []string
	input           string
	errorText       string
	editing         bool
	pending         bool
}

type groupingUpdate struct {
	nodes           treeNodes
	groupLeaves     []*Node
	groupLabelNames []string
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
	groupLabelNames, err := parseGroupingLabels(grouping.input, grouping.allowedLabels)
	if err != nil {
		grouping.errorText = err.Error()
		return nil
	}

	grouping.editing = false
	if slices.Equal(groupLabelNames, grouping.groupLabelNames) {
		return nil
	}
	if len(groupLabelNames) == 0 {
		m.applyGrouping(&groupingUpdate{nodes: grouping.ungroupedTree})
		return nil
	}

	grouping.pending = true
	ctx := grouping.ctx
	previous := m.tree
	order := groupingOrder(grouping.groupLabelNames, groupLabelNames)
	return func() tea.Msg {
		return grouping.build(ctx, groupLabelNames, previous, order)
	}
}

func parseGroupingLabels(input string, allowedLabels []string) ([]string, error) {
	if strings.TrimSpace(input) == "" {
		return nil, nil
	}

	var groupLabelNames []string
	for field := range strings.SplitSeq(input, ",") {
		groupLabel := strings.TrimSpace(field)
		if !slices.Contains(allowedLabels, groupLabel) {
			return nil, fmt.Errorf("invalid grouping label %q (use %s)", groupLabel, strings.Join(allowedLabels, ", "))
		}
		if slices.Contains(groupLabelNames, groupLabel) {
			return nil, fmt.Errorf("duplicate grouping label %q", groupLabel)
		}

		groupLabelNames = append(groupLabelNames, groupLabel)
	}
	return groupLabelNames, nil
}

func (g *groupingState) build(
	ctx context.Context, groupLabelNames []string, previous treeNodes, order []int,
) groupingUpdate {
	g.buildMu.Lock()
	defer g.buildMu.Unlock()
	// A queued command can start after the viewer has already closed.
	if err := ctx.Err(); err != nil {
		return groupingUpdate{err: err}
	}

	if order != nil {
		nodes, groupLeaves, err := reorderGroupLabels(ctx, previous, order)
		if err != nil {
			return groupingUpdate{err: fmt.Errorf("reorder groups by %s: %w", strings.Join(groupLabelNames, ","), err)}
		}

		return groupingUpdate{
			nodes:           nodes,
			groupLeaves:     groupLeaves,
			groupLabelNames: groupLabelNames,
		}
	}

	tree, err := g.buildTree(ctx, groupLabelNames)
	if err != nil {
		return groupingUpdate{err: fmt.Errorf("group by %s: %w", strings.Join(groupLabelNames, ","), err)}
	}
	nodes, err := tree.buildNodes()
	if err != nil {
		return groupingUpdate{err: fmt.Errorf("group by %s: %w", strings.Join(groupLabelNames, ","), err)}
	}
	if len(nodes) == 0 {
		return groupingUpdate{err: fmt.Errorf("group by %s: no weighted samples", strings.Join(groupLabelNames, ","))}
	}

	return groupingUpdate{
		nodes:           nodes,
		groupLabelNames: groupLabelNames,
	}
}

func (m *Model) applyGrouping(update *groupingUpdate) {
	if m.quitting {
		return
	}
	grouping := m.grouping
	grouping.pending = false
	if update.err != nil {
		// Worker errors can include report text. Input errors already quote labels.
		grouping.errorText = terminalText(update.err.Error())
		return
	}

	// The old view remains readable until the event loop installs the new one.
	for _, parent := range update.groupLeaves {
		for _, child := range parent.Children {
			child.Parent = parent
		}
	}

	grouping.errorText = ""
	grouping.groupLabelNames = update.groupLabelNames
	m.tree = update.nodes
	m.focusOn(m.tree.root())
}

func (g *groupingState) status() string {
	if g.errorText != "" {
		return g.errorText
	}
	if g.editing {
		return "group by: " + terminalText(g.input) + "_ (" + strings.Join(g.allowedLabels, ",") + " | empty clears)"
	}
	if g.pending {
		return "grouping stacks…"
	}

	current := strings.Join(g.groupLabelNames, ",")
	if len(g.groupLabelNames) == 0 {
		current = "none"
	}
	return "groups: " + current + " (g edit)"
}

// groupingOrder maps each requested groupLabel to its previous position. A nil order
// means the grouping fields changed and the samples must be grouped again.
func groupingOrder(previous, groupLabelNames []string) []int {
	if len(previous) == 0 || len(previous) != len(groupLabelNames) {
		return nil
	}

	order := make([]int, len(groupLabelNames))
	for index, groupLabel := range groupLabelNames {
		order[index] = slices.Index(previous, groupLabel)
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
	groupLeaves := make([]*Node, 0, len(subtrees))
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
		groupLeaves = append(groupLeaves, label)
	}
	return nodes, groupLeaves, nil
}

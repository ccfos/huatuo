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

// Package tui renders profiling stacks in an interactive terminal viewer.
package tui

import (
	"context"
	"errors"
	"iter"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	defaultWidth  = 100
	defaultHeight = 30
)

type viewMode uint8

const (
	viewGraph viewMode = iota
	viewFrames
	viewTopTable
)

// Node is one flamegraph frame.
// Level is the depth from the original synthetic root, even when that root is removed.
// Value is inclusive sample weight and Self is exclusive sample weight.
type Node struct {
	Label    string
	Level    int64
	Value    int64
	Self     int64
	Parent   *Node
	Children []*Node
}

// Run opens the terminal viewer and reports whether the user quit with q.
// A nil model opens an empty viewer.
func Run(ctx context.Context, model *Model) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if model == nil {
		model = newModel(treeNodes{{Label: "empty"}})
	}

	// Bubble Tea needs a canceled context to restore the terminal on these signals.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGHUP, syscall.SIGQUIT)
	defer stop()

	if grouping := model.grouping; grouping != nil {
		var cancel context.CancelFunc
		grouping.ctx, cancel = context.WithCancel(ctx)
		defer func() {
			// Bubble Tea does not wait for running commands on shutdown.
			cancel()
			grouping.buildMu.Lock()
			defer grouping.buildMu.Unlock()
		}()
	}

	result, err := tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(ctx)).Run()
	if ctx.Err() != nil && errors.Is(err, ctx.Err()) && errors.Is(err, tea.ErrProgramKilled) {
		return false, ctx.Err()
	}
	final, ok := result.(Model)
	return ok && final.quitWithQ, err
}

// treeNodes lists a root and its descendants in preorder.
type treeNodes []*Node

func (nodes treeNodes) root() *Node {
	if len(nodes) == 0 {
		return nil
	}
	return nodes[0]
}

// Model is the Bubble Tea state for the flamegraph viewer.
type Model struct {
	view       viewMode
	tree       treeNodes
	visible    treeNodes
	cursor     int
	offset     int
	width      int
	height     int
	searching  bool
	query      string
	lastSearch string
	matches    map[*Node]bool
	grouping   *groupingState
	topTable   *topTableModel
	quitWithQ  bool
}

// newModel takes ownership of nodes in preorder.
func newModel(nodes treeNodes) *Model {
	return &Model{
		tree:    nodes,
		visible: nodes,
		width:   defaultWidth,
		height:  defaultHeight,
		matches: map[*Node]bool{},
	}
}

func (m Model) Init() tea.Cmd { //nolint:gocritic // Update returns Model values to Bubble Tea.
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) { //nolint:gocritic // Update returns Model values to Bubble Tea.
	var cmd tea.Cmd
	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = typed.Width
		m.height = typed.Height
		m.clampCursor()
	case groupingResult:
		m.applyGrouping(&typed)
	case tea.KeyMsg:
		switch {
		case m.grouping != nil && m.grouping.editing && typed.Type != tea.KeyCtrlC:
			cmd = m.updateGrouping(typed)
		case m.searching:
			m.updateSearch(typed)
		default:
			cmd = m.updateKey(typed)
		}
	}
	return m, cmd
}

func (m *Model) updateKey(key tea.KeyMsg) tea.Cmd {
	name := key.String()
	switch name {
	case "q":
		m.quitWithQ = true
		return tea.Quit
	case "ctrl+c", "esc":
		return tea.Quit
	case "v":
		m.cycleView()
		return nil
	case "g":
		if grouping := m.grouping; grouping != nil && m.view != viewTopTable && !grouping.pending {
			grouping.editing = true
			grouping.input = strings.Join(grouping.tags, ",")
			grouping.errorText = ""
		}
		return nil
	case "r":
		m.view = viewGraph
		m.focusOn(m.tree.root())
		return nil
	}
	if m.navigate(name) {
		return nil
	}
	if m.view == viewTopTable {
		if name == "s" {
			m.topTable.sortByTotal = !m.topTable.sortByTotal
			m.topTable.sortRows()
		}
		return nil
	}

	switch name {
	case "right", "l":
		m.moveHorizontal(1)
	case "left", "h":
		m.moveHorizontal(-1)
	case "enter":
		m.zoomIn()
	case "backspace":
		m.zoomOut()
	case "/":
		m.searching = true
		m.query = ""
	case "n":
		m.nextMatch()
	}
	return nil
}

func (m *Model) cycleView() {
	switch m.view {
	case viewGraph:
		m.view = viewFrames
	case viewFrames:
		m.view = viewGraph
		if m.topTable != nil {
			m.view = viewTopTable
		}
	case viewTopTable:
		m.view = viewGraph
	}
}

func (m *Model) navigate(key string) bool {
	if m.view == viewGraph {
		switch key {
		case "up", "k":
			m.moveVertical(-1)
			return true
		case "down", "j":
			m.moveVertical(1)
			return true
		}
	}

	cursor, count, page := &m.cursor, len(m.visible), m.viewportHeight()
	if m.view == viewTopTable {
		cursor, count, page = &m.topTable.cursor, len(m.topTable.rows), m.topTableHeight()
	}
	switch key {
	case "up", "k":
		*cursor--
	case "down", "j":
		*cursor++
	case "pgup":
		*cursor -= page
	case "pgdown":
		*cursor += page
	case "home":
		*cursor = 0
	case "end":
		*cursor = count - 1
	default:
		return false
	}

	*cursor = max(0, min(*cursor, count-1))
	if m.view != viewTopTable {
		m.ensureCursorVisible()
	}
	return true
}

func (m *Model) moveVertical(delta int) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return
	}

	selected := m.visible[m.cursor]
	switch {
	case delta > 0 && len(selected.Children) > 0:
		// The first child immediately follows its parent in preorder.
		m.cursor++
	case delta > 0:
		m.cursor = 0
	default:
		for index := m.cursor - 1; index >= 0; index-- {
			if m.visible[index] == selected.Parent {
				m.cursor = index
				break
			}
		}
	}
	m.ensureCursorVisible()
}

func (m *Model) moveHorizontal(delta int) {
	focus := m.visible.root()
	selected := m.selectedNode()
	if selected == nil || selected == focus {
		return
	}

	// The visible list is depth-first; skip nodes at other depths.
	for index := m.cursor + delta; index >= 0 && index < len(m.visible); index += delta {
		if m.visible[index].Level == selected.Level {
			m.cursor = index
			m.ensureCursorVisible()
			return
		}
	}
}

func (m *Model) zoomIn() {
	selected := m.selectedNode()
	if selected == nil || len(selected.Children) == 0 {
		return
	}
	if selected != m.visible.root() {
		// A subtree occupies a contiguous range of the preorder list.
		end := m.cursor + 1
		for end < len(m.visible) && m.visible[end].Level > selected.Level {
			end++
		}
		m.visible = m.visible[m.cursor:end:end]
	}
	m.cursor, m.offset = 0, 0
	m.refreshMatches()
}

func (m *Model) zoomOut() {
	previous := m.visible.root()
	if previous == nil || previous.Parent == nil {
		return
	}
	m.refreshVisible(previous.Parent)
	if index := slices.Index(m.visible, previous); index >= 0 {
		m.cursor = index
	}
	m.ensureCursorVisible()
}

func (m *Model) updateSearch(key tea.KeyMsg) {
	switch key.String() {
	case "ctrl+c", "esc":
		m.searching = false
		m.query = ""
	case "enter":
		m.searching = false
		m.applySearch()
	case "backspace":
		_, size := utf8.DecodeLastRuneInString(m.query)
		m.query = m.query[:len(m.query)-size]
	case "space":
		m.query += " "
	default:
		if len(key.Runes) > 0 {
			m.query += string(key.Runes)
		}
	}
}

func (m *Model) applySearch() {
	m.lastSearch = strings.TrimSpace(m.query)
	firstMatch := m.refreshMatches()
	if firstMatch >= 0 {
		m.cursor = firstMatch
		m.ensureCursorVisible()
	}
}

func (m *Model) refreshMatches() int {
	m.matches = map[*Node]bool{}
	if m.lastSearch == "" {
		return -1
	}
	needle := strings.ToLower(m.lastSearch)
	firstMatch := -1
	for index, node := range m.visible {
		if strings.Contains(strings.ToLower(node.Label), needle) {
			m.matches[node] = true
			if firstMatch == -1 {
				firstMatch = index
			}
		}
	}
	return firstMatch
}

func (m *Model) nextMatch() {
	if len(m.matches) == 0 || len(m.visible) == 0 {
		return
	}
	for step := 1; step <= len(m.visible); step++ {
		index := (m.cursor + step) % len(m.visible)
		if m.matches[m.visible[index]] {
			m.cursor = index
			m.ensureCursorVisible()
			return
		}
	}
}

func (m *Model) focusOn(node *Node) {
	m.cursor = 0
	m.offset = 0
	m.refreshVisible(node)
}

func (m *Model) refreshVisible(root *Node) {
	m.visible = m.tree
	if root != m.tree.root() {
		m.visible = slices.Collect(walkPreorder(root))
	}
	m.refreshMatches()
	m.clampCursor()
}

func walkPreorder(root *Node) iter.Seq[*Node] {
	return func(yield func(*Node) bool) {
		if root == nil {
			return
		}

		pending := []*Node{root}
		for len(pending) > 0 {
			last := len(pending) - 1
			node := pending[last]
			pending = pending[:last]
			if !yield(node) {
				return
			}

			for _, child := range slices.Backward(node.Children) {
				pending = append(pending, child)
			}
		}
	}
}

func (m *Model) selectedNode() *Node {
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		return m.visible.root()
	}
	return m.visible[m.cursor]
}

func (m *Model) clampCursor() {
	m.cursor = max(0, min(m.cursor, len(m.visible)-1))
	m.ensureCursorVisible()
}

func (m *Model) ensureCursorVisible() {
	depth := 0
	if selected := m.selectedNode(); selected != nil {
		depth = int(selected.Level - m.visible.root().Level)
	}
	viewport := max(m.viewportHeight(), 1)
	minOffset := max(0, depth-viewport+1)
	m.offset = max(minOffset, min(m.offset, depth))
}

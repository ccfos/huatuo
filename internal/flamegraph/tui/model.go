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

// Package tui renders flamegraph FrameData in an interactive terminal viewer.
package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"huatuo-bamai/internal/flamegraph"
)

const (
	defaultWidth  = 100
	defaultHeight = 30
	barMinWidth   = 1
	headerLines   = 4
	footerLines   = 5
)

// Node is one reconstructed flamegraph frame.
type Node struct {
	flamegraph.FrameData
	Parent   *Node
	Children []*Node
}

// Run opens the interactive terminal viewer.
func Run(data []flamegraph.FrameData) error {
	program := tea.NewProgram(NewModel(data), tea.WithAltScreen())
	_, err := program.Run()
	return err
}

// Model is the Bubble Tea state for the flamegraph viewer.
type Model struct {
	root       *Node
	focus      *Node
	visible    []*Node
	cursor     int
	offset     int
	width      int
	height     int
	searching  bool
	query      string
	lastSearch string
	matches    map[*Node]bool
}

// NewModel creates a TUI model from nested-set flamegraph data.
func NewModel(data []flamegraph.FrameData) Model {
	root := BuildTree(data)
	model := Model{
		root:    root,
		focus:   root,
		width:   defaultWidth,
		height:  defaultHeight,
		matches: map[*Node]bool{},
	}
	model.refreshVisible()
	return model
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

	if len(roots) == 0 {
		return &Node{FrameData: flamegraph.FrameData{Label: "empty", Value: 0}}
	}
	if len(roots) == 1 {
		return roots[0]
	}

	root := &Node{FrameData: flamegraph.FrameData{Label: "all"}}
	for _, child := range roots {
		child.Parent = root
		root.Children = append(root.Children, child)
		root.Value += child.Value
		root.Self += child.Self
	}
	return root
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch typed := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = typed.Width
		m.height = typed.Height
		m.clampCursor()
	case tea.KeyMsg:
		if m.searching {
			return m.updateSearch(typed), nil
		}
		updated, cmd := m.updateKey(typed)
		return updated, cmd
	}
	return m, nil
}

func (m Model) updateKey(key tea.KeyMsg) (Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c", "q", "esc":
		return m, tea.Quit
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "pgup":
		m.moveCursor(-m.viewportHeight())
	case "pgdown":
		m.moveCursor(m.viewportHeight())
	case "home":
		m.cursor = 0
		m.offset = 0
	case "end":
		m.cursor = len(m.visible) - 1
		m.ensureCursorVisible()
	case "enter", "right", "l":
		m.zoomIn()
	case "backspace", "left", "h":
		m.zoomOut()
	case "/":
		m.searching = true
		m.query = ""
	case "n":
		m.nextMatch()
	}
	return m, nil
}

func (m Model) updateSearch(key tea.KeyMsg) Model {
	switch key.String() {
	case "ctrl+c", "esc":
		m.searching = false
		m.query = ""
	case "enter":
		m.searching = false
		m.applySearch()
	case "backspace":
		if len(m.query) > 0 {
			m.query = m.query[:len(m.query)-1]
		}
	case "space":
		m.query += " "
	default:
		if len(key.Runes) > 0 {
			m.query += string(key.Runes)
		}
	}
	return m
}

func (m Model) View() string {
	var out strings.Builder
	selected := m.selectedNode()
	rootValue := m.focus.Value
	if rootValue <= 0 {
		rootValue = selected.Value
	}

	fmt.Fprintf(&out, "HUATUO perf flamegraph  frames=%d  focus=%s\n", len(m.visible), m.focus.Label)
	fmt.Fprintf(&out, "keys: up/down scroll  enter zoom  backspace back  / search  n next  q quit\n")
	if m.searching {
		fmt.Fprintf(&out, "search: %s_\n", m.query)
	} else if m.lastSearch != "" {
		fmt.Fprintf(&out, "search: %s  matches=%d\n", m.lastSearch, len(m.matches))
	} else {
		out.WriteString("search: none\n")
	}
	out.WriteString(strings.Repeat("-", m.safeWidth()) + "\n")

	viewport := m.viewportHeight()
	end := m.offset + viewport
	if end > len(m.visible) {
		end = len(m.visible)
	}
	for index := m.offset; index < end; index++ {
		node := m.visible[index]
		cursor := " "
		if index == m.cursor {
			cursor = ">"
		}
		match := " "
		if m.matches[node] {
			match = "*"
		}
		out.WriteString(m.renderRow(cursor, match, node, rootValue))
		out.WriteByte('\n')
	}
	for index := end - m.offset; index < viewport; index++ {
		out.WriteByte('\n')
	}

	out.WriteString(strings.Repeat("-", m.safeWidth()) + "\n")
	out.WriteString(renderDetails(selected, m.focus.Value))
	return out.String()
}

func (m Model) renderRow(cursor, match string, node *Node, rootValue int64) string {
	depth := nodeDepthFrom(node, m.focus)
	indent := strings.Repeat("  ", depth)
	barWidth := m.barWidth(node.Value, rootValue)
	percent := percent(node.Value, rootValue)
	labelWidth := m.safeWidth() - len(cursor) - len(match) - len(indent) - barWidth - 16
	if labelWidth < 12 {
		labelWidth = 12
	}
	label := truncate(node.Label, labelWidth)
	bar := strings.Repeat("#", barWidth)
	return fmt.Sprintf("%s%s%s%s %-*s %6.2f%%", cursor, match, indent, bar, labelWidth, label, percent)
}

func renderDetails(node *Node, focusValue int64) string {
	if node == nil {
		return "selected: none\n"
	}
	return fmt.Sprintf("selected: %s\nvalue: %d  self: %d  focus: %.2f%%  children: %d\npath: %s\n",
		node.Label, node.Value, node.Self, percent(node.Value, focusValue), len(node.Children), nodePath(node))
}

func (m *Model) refreshVisible() {
	m.visible = flatten(m.focus)
	m.clampCursor()
}

func flatten(root *Node) []*Node {
	if root == nil {
		return nil
	}
	result := []*Node{root}
	for _, child := range root.Children {
		result = append(result, flatten(child)...)
	}
	return result
}

func (m *Model) moveCursor(delta int) {
	m.cursor += delta
	m.clampCursor()
	m.ensureCursorVisible()
}

func (m *Model) clampCursor() {
	if len(m.visible) == 0 {
		m.cursor = 0
		m.offset = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.visible) {
		m.cursor = len(m.visible) - 1
	}
	m.ensureCursorVisible()
}

func (m *Model) ensureCursorVisible() {
	viewport := m.viewportHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+viewport {
		m.offset = m.cursor - viewport + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func (m *Model) zoomIn() {
	selected := m.selectedNode()
	if selected == nil || len(selected.Children) == 0 {
		return
	}
	m.focus = selected
	m.cursor = 0
	m.offset = 0
	m.refreshVisible()
}

func (m *Model) zoomOut() {
	if m.focus == nil || m.focus.Parent == nil {
		return
	}
	previous := m.focus
	m.focus = m.focus.Parent
	m.refreshVisible()
	for index, node := range m.visible {
		if node == previous {
			m.cursor = index
			break
		}
	}
	m.ensureCursorVisible()
}

func (m *Model) selectedNode() *Node {
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		return m.focus
	}
	return m.visible[m.cursor]
}

func (m *Model) applySearch() {
	m.lastSearch = strings.TrimSpace(m.query)
	m.matches = map[*Node]bool{}
	if m.lastSearch == "" {
		return
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
	if firstMatch >= 0 {
		m.cursor = firstMatch
		m.ensureCursorVisible()
	}
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

func (m Model) viewportHeight() int {
	height := m.height - headerLines - footerLines
	if height < 3 {
		return 3
	}
	return height
}

func (m Model) safeWidth() int {
	if m.width < 40 {
		return 40
	}
	return m.width
}

func (m Model) barWidth(value, rootValue int64) int {
	available := m.safeWidth() / 3
	if available < 10 {
		available = 10
	}
	if rootValue <= 0 || value <= 0 {
		return barMinWidth
	}
	width := int(value * int64(available) / rootValue)
	if width < barMinWidth {
		return barMinWidth
	}
	if width > available {
		return available
	}
	return width
}

func percent(value, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return float64(value) * 100 / float64(total)
}

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}

func nodeDepthFrom(node, root *Node) int {
	depth := 0
	for current := node; current != nil && current != root; current = current.Parent {
		depth++
	}
	return depth
}

func nodePath(node *Node) string {
	var labels []string
	for current := node; current != nil; current = current.Parent {
		labels = append(labels, current.Label)
	}
	for left, right := 0, len(labels)-1; left < right; left, right = left+1, right-1 {
		labels[left], labels[right] = labels[right], labels[left]
	}
	return strings.Join(labels, " > ")
}

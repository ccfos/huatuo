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
	"fmt"
	"math/bits"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/ccfos/huatuo/internal/profiling/flamegraph"
)

const (
	defaultWidth  = 100
	defaultHeight = 30
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
	model := newModel(BuildTree(data))
	_, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
	return err
}

// Model is the Bubble Tea state for the flamegraph viewer.
type Model struct {
	isListView bool
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

func newModel(root *Node) Model {
	model := Model{
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
	}
	return root
}

func (m Model) Init() tea.Cmd { //nolint:gocritic // tea.Model interface requires value receiver
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) { //nolint:gocritic // tea.Model interface requires value receiver
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

func (m *Model) updateKey(key tea.KeyMsg) (Model, tea.Cmd) {
	switch key.String() {
	case "ctrl+c", "q", "esc":
		return *m, tea.Quit
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "pgup":
		m.moveCursor(-m.viewportHeight())
	case "pgdown":
		m.moveCursor(m.viewportHeight())
	case "v":
		m.isListView = !m.isListView
	case "home":
		m.cursor = 0
		m.offset = 0
	case "end":
		m.cursor = len(m.visible) - 1
		m.ensureCursorVisible()
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
	return *m, nil
}

func (m *Model) moveCursor(delta int) {
	m.cursor += delta
	m.clampCursor()
	m.ensureCursorVisible()
}

func (m *Model) moveHorizontal(delta int) {
	selected := m.selectedNode()
	if selected == nil || selected == m.focus {
		return
	}

	depth := nodeDepthFrom(selected, m.focus)
	// The visible list is depth-first; skip nodes at other depths.
	for index := m.cursor + delta; index >= 0 && index < len(m.visible); index += delta {
		if nodeDepthFrom(m.visible[index], m.focus) == depth {
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

func (m *Model) updateSearch(key tea.KeyMsg) Model {
	switch key.String() {
	case "ctrl+c", "esc":
		m.searching = false
		m.query = ""
	case "enter":
		m.searching = false
		m.applySearch()
	case "backspace":
		if m.query != "" {
			m.query = m.query[:len(m.query)-1]
		}
	case "space":
		m.query += " "
	default:
		if len(key.Runes) > 0 {
			m.query += string(key.Runes)
		}
	}
	return *m
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

func (m *Model) refreshVisible() {
	m.visible = flatten(m.focus)
	m.refreshMatches()
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

func (m *Model) selectedNode() *Node {
	if len(m.visible) == 0 || m.cursor < 0 || m.cursor >= len(m.visible) {
		return m.focus
	}
	return m.visible[m.cursor]
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
	depth := nodeDepthFrom(m.selectedNode(), m.focus)
	viewport := max(m.viewportHeight(), 1)
	if depth < m.offset {
		m.offset = depth
	}
	if depth >= m.offset+viewport {
		m.offset = depth - viewport + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func nodeDepthFrom(node, root *Node) int {
	depth := 0
	for current := node; current != nil && current != root; current = current.Parent {
		depth++
	}
	return depth
}

func (m *Model) viewportHeight() int {
	return max(m.height-headerLines-footerLines, 0)
}

func (m *Model) safeWidth() int {
	return max(m.width, 1)
}

func (m Model) View() string { //nolint:gocritic // tea.Model interface requires value receiver
	var content, view string
	if m.isListView {
		content = m.renderList()
		view = "all frames (v: graph)"
	} else {
		var shown int
		content, shown = m.renderFlamegraph()
		view = fmt.Sprintf("graph, %d shown (v: ALL frames, including tiny)", shown)
	}

	var out strings.Builder
	selected := m.selectedNode()
	fmt.Fprintf(&out, "HUATUO tui flamegraph  frames=%d  focus=%s\n", len(m.visible), terminalText(m.focus.Label))
	fmt.Fprintf(&out, "keys: v view  up/down select  left/right level  enter zoom  backspace back  / find  n next  q quit\n")
	fmt.Fprintf(&out, "view: %s | ", view)
	if m.searching {
		fmt.Fprintf(&out, "search: %s_\n", terminalText(m.query))
	} else if m.lastSearch != "" {
		fmt.Fprintf(&out, "search: %s  matches=%d\n", terminalText(m.lastSearch), len(m.matches))
	} else {
		out.WriteString("search: none\n")
	}
	out.WriteString(strings.Repeat("-", m.safeWidth()) + "\n")

	out.WriteString(content)

	out.WriteString(strings.Repeat("-", m.safeWidth()) + "\n")
	out.WriteString(renderDetails(selected, m.focus.Value))
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	lines = lines[:min(len(lines), max(m.height, 1))]
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], m.safeWidth(), "")
	}

	return strings.Join(lines, "\n") + "\n"
}

// renderFlamegraph packs siblings horizontally and places children one row below.
func (m *Model) renderFlamegraph() (string, int) {
	rows := make([]strings.Builder, m.viewportHeight())
	columns := make([]int, len(rows))
	shown := 0
	selected := m.selectedNode()
	var draw func(node *Node, left, right, depth int)
	draw = func(node *Node, left, right, depth int) {
		if left >= right || depth >= m.offset+len(rows) || node.Value <= 0 {
			return
		}
		if depth >= m.offset {
			row := depth - m.offset
			rows[row].WriteString(strings.Repeat(" ", left-columns[row]))
			rows[row].WriteString(m.renderBlock(node, right-left, node == selected))
			columns[row] = right
			shown++
		}

		// Cumulative boundaries keep adjacent blocks aligned and preserve self time.
		consumed := min(max(node.Self, 0), node.Value)
		for _, child := range node.Children {
			start := left + frameColumn(consumed, node.Value, right-left)
			consumed += min(max(child.Value, 0), node.Value-consumed)
			end := left + frameColumn(consumed, node.Value, right-left)
			draw(child, start, end, depth+1)
		}
	}
	draw(m.focus, 0, m.safeWidth(), 0)

	var out strings.Builder
	for i := range rows {
		out.WriteString(rows[i].String())
		out.WriteByte('\n')
	}

	return out.String(), shown
}

// renderList exposes every frame independently of its proportional graph width.
func (m *Model) renderList() string {
	viewport := m.viewportHeight()
	start := max(0, m.cursor-viewport+1)
	end := min(len(m.visible), start+viewport)
	var out strings.Builder
	for index := start; index < end; index++ {
		node := m.visible[index]
		marker := "  "
		if m.matches[node] {
			marker = "* "
		}
		if index == m.cursor {
			marker = "> "
		}
		suffix := fmt.Sprintf(" %.2f%%", percent(node.Value, m.focus.Value))
		labelWidth := max(0, m.safeWidth()-len(marker)-len(suffix))
		label := ansi.Truncate(terminalText(node.Label), labelWidth, "")
		padding := strings.Repeat(" ", labelWidth-ansi.StringWidth(label))
		line := marker + label + padding + suffix
		out.WriteString(ansi.Truncate(line, m.safeWidth(), ""))
		out.WriteByte('\n')
	}
	for index := end - start; index < viewport; index++ {
		out.WriteByte('\n')
	}

	return out.String()
}

func (m *Model) renderBlock(node *Node, width int, selected bool) string {
	label := terminalText(node.Label)
	var hash uint32
	for _, char := range label {
		hash = hash*31 + uint32(char)
	}
	style := lipgloss.NewStyle().
		Background(lipgloss.Color(fmt.Sprint(166 + hash%12))).
		Foreground(lipgloss.Color("0"))
	if m.matches[node] {
		style = style.Background(lipgloss.Color("226"))
	}
	if selected {
		label = ">" + label
		style = style.Background(lipgloss.Color("33")).Foreground(lipgloss.Color("15"))
	}
	label = ansi.Truncate(truncate(label, width), width, "")
	label += strings.Repeat(" ", width-ansi.StringWidth(label))

	return style.Render(label)
}

func renderDetails(node *Node, focusValue int64) string {
	if node == nil {
		return "selected: none\n"
	}
	return fmt.Sprintf("selected: %s\nvalue: %d  self: %d  focus: %.2f%%  children: %d\npath: %s\n",
		terminalText(node.Label), node.Value, node.Self, percent(node.Value, focusValue), len(node.Children), terminalText(nodePath(node)))
}

// frameColumn scales weights without overflowing or losing int64 precision.
func frameColumn(value, total int64, width int) int {
	if value <= 0 || total <= 0 {
		return 0
	}
	if value >= total {
		return width
	}
	hi, lo := bits.Mul64(uint64(value), uint64(width))
	column, _ := bits.Div64(hi, lo, uint64(total))
	return int(column)
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

// terminalText makes untrusted text printable before styling or truncation.
func terminalText(value string) string {
	quoted := strconv.Quote(value)
	return quoted[1 : len(quoted)-1]
}

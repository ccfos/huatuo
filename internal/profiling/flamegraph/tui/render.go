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
	"fmt"
	"math/bits"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	headerLines = 4
	footerLines = 5
)

func (m *Model) viewportHeight() int {
	height := m.height - headerLines - footerLines
	if m.grouping != nil {
		height--
	}
	return max(height, 0)
}

func (m *Model) safeWidth() int {
	return max(m.width, 1)
}

func (m Model) View() string { //nolint:gocritic // Update returns Model values to Bubble Tea.
	if m.view == viewTopTable {
		return m.fitView(m.renderTopTable())
	}
	focus := m.visible.root()
	if focus == nil {
		return ""
	}

	var content, view string
	verticalKeys := "↑ parent  ↓ child/root"
	if m.view == viewFrames {
		content = m.renderList(focus.Value)
		verticalKeys = "↑/↓ select"
		view = "all frames (v: graph)"
		if m.topTable != nil {
			view = "all frames (v: Top Table)"
		}
	} else {
		var shown int
		content, shown = m.renderFlamegraph()
		view = fmt.Sprintf("graph, %d shown (v: ALL frames, including tiny)", shown)
	}

	var out strings.Builder
	selected := m.selectedNode()
	fmt.Fprintf(&out, "HUATUO tui flamegraph  frames=%d  focus=%s\n", len(m.visible), terminalText(focus.Label))
	fmt.Fprintf(&out, "keys: v view  %s  ←/→ level  enter zoom  backspace back  r root  / find  n next  q quit\n", verticalKeys)
	if m.grouping != nil {
		out.WriteString(m.grouping.status() + "\n")
	}
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
	out.WriteString(renderDetails(selected, focus.Value))
	return m.fitView(out.String())
}

func (m *Model) fitView(rendered string) string {
	lines := strings.Split(strings.TrimSuffix(rendered, "\n"), "\n")
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
	width := m.safeWidth()
	focus := m.visible.root()
	selected := m.selectedNode()
	var selectedPath []*Node
	for node := selected; node != nil; node = node.Parent {
		selectedPath = append(selectedPath, node)
		if node == focus {
			break
		}
	}
	slices.Reverse(selectedPath)
	cursorRow := len(selectedPath) - 1 - m.offset
	cursorColumn := -1

	var draw func(node *Node, left, right, depth int)
	draw = func(node *Node, left, right, depth int) {
		if depth >= m.offset+len(rows) || node.Value <= 0 {
			return
		}
		if left >= right {
			// Every descendant of a zero-width frame shares its column boundary.
			if depth < len(selectedPath) && selectedPath[depth] == node {
				cursorColumn = min(left, width-1)
			}
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
		start := left + frameColumn(consumed, node.Value, right-left)
		for _, child := range node.Children {
			consumed += min(max(child.Value, 0), node.Value-consumed)
			end := left + frameColumn(consumed, node.Value, right-left)
			draw(child, start, end, depth+1)
			start = end
		}
	}
	if focus != nil {
		draw(focus, 0, width, 0)
	}

	var out strings.Builder
	for i := range rows {
		line := rows[i].String()
		if i == cursorRow && cursorColumn >= 0 {
			line = overlayCursor(line, cursorColumn, m.renderBlock(selected, 1, true))
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}

	return out.String(), shown
}

// overlayCursor replaces a terminal cell without shifting the following text.
func overlayCursor(line string, column int, cursor string) string {
	start := 0
	for rest := line; rest != ""; {
		_, width, size, _ := ansi.DecodeSequence(rest, ansi.NormalState, nil)
		end := start + width
		if column < end {
			// Clear every cell occupied by the overwritten grapheme.
			before := ansi.Truncate(line, start, "") + strings.Repeat(" ", column-start)
			after := strings.Repeat(" ", end-column-1) + ansi.TruncateLeft(line, end, "")
			return before + cursor + after
		}
		start = end
		rest = rest[size:]
	}
	return line + strings.Repeat(" ", column-start) + cursor
}

// renderList exposes every frame independently of its proportional graph width.
func (m *Model) renderList(focusValue int64) string {
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
		suffix := fmt.Sprintf(" %.2f%%", percent(node.Value, focusValue))
		labelWidth := max(0, m.safeWidth()-len(marker)-len(suffix))
		out.WriteString(marker + padLabel(node.Label, labelWidth) + suffix + "\n")
	}
	out.WriteString(strings.Repeat("\n", viewport-(end-start)))

	return out.String()
}

func padLabel(name string, width int) string {
	label := ansi.Truncate(terminalText(name), width, "")
	return label + strings.Repeat(" ", width-ansi.StringWidth(label))
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
	tail := ""
	if width > 3 {
		tail = "..."
	}
	label = ansi.Truncate(label, width, tail)
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

func nodePath(node *Node) string {
	var labels []string
	for current := node; current != nil; current = current.Parent {
		labels = append(labels, current.Label)
	}
	slices.Reverse(labels)
	return strings.Join(labels, " > ")
}

// terminalText makes untrusted text printable before styling or truncation.
func terminalText(value string) string {
	quoted := strconv.Quote(value)
	return strings.ReplaceAll(quoted[1:len(quoted)-1], `\"`, `"`)
}

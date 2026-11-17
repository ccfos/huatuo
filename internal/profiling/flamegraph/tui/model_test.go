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
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ccfos/huatuo/internal/profiling/flamegraph"
)

func sampleFrames() []flamegraph.FrameData {
	return []flamegraph.FrameData{
		{Level: 0, Value: 100, Self: 0, Label: "root"},
		{Level: 1, Value: 70, Self: 20, Label: "runtime.main"},
		{Level: 2, Value: 40, Self: 40, Label: "handleRequest"},
		{Level: 1, Value: 30, Self: 30, Label: "syscall"},
	}
}

func TestBuildTree(t *testing.T) {
	root := BuildTree(sampleFrames())
	if root.Label != "root" {
		t.Fatalf("root label = %q, want root", root.Label)
	}
	if len(root.Children) != 2 {
		t.Fatalf("root children = %d, want 2", len(root.Children))
	}
	if root.Children[0].Children[0].Label != "handleRequest" {
		t.Fatalf("nested label = %q, want handleRequest", root.Children[0].Children[0].Label)
	}
}

func TestBuildTreeWithMultipleRoots(t *testing.T) {
	root := BuildTree([]flamegraph.FrameData{
		{Level: 0, Value: 60, Self: 10, Label: "pid-a"},
		{Level: 1, Value: 50, Self: 50, Label: "work-a"},
		{Level: 0, Value: 40, Self: 15, Label: "pid-b"},
		{Level: 1, Value: 25, Self: 25, Label: "work-b"},
	})
	if root.Label != "all" {
		t.Fatalf("root label = %q, want all", root.Label)
	}
	if root.Value != 100 {
		t.Fatalf("root value = %d, want 100", root.Value)
	}
	if root.Self != 0 {
		t.Fatalf("synthetic root self = %d, want 0", root.Self)
	}
	if len(root.Children) != 2 {
		t.Fatalf("root children = %d, want 2", len(root.Children))
	}
}

func TestModelSearchAndZoom(t *testing.T) {
	model := NewModel(sampleFrames())
	model.width = 80
	model.height = 20

	model.query = "runtime"
	model.applySearch()
	if selected := model.selectedNode(); selected.Label != "runtime.main" {
		t.Fatalf("selected after search = %q, want runtime.main", selected.Label)
	}

	model, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	if model.focus.Label != "runtime.main" {
		t.Fatalf("focus after zoom = %q, want runtime.main", model.focus.Label)
	}

	model, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if model.focus.Label != "root" {
		t.Fatalf("focus after zoom out = %q, want root", model.focus.Label)
	}
}

func TestModelViewIncludesDetails(t *testing.T) {
	model := NewModel(sampleFrames())
	model.width = 80
	model.height = 20
	view := model.View()
	for _, want := range []string{"HUATUO perf flamegraph", "runtime.main", "selected:", "path:"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q in:\n%s", want, view)
		}
	}
}

func TestSearchInputUsesRunesOnly(t *testing.T) {
	model := NewModel(sampleFrames())
	model.searching = true
	model = model.updateSearch(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sys")})
	model = model.updateSearch(tea.KeyMsg{Type: tea.KeyCtrlA})
	if model.query != "sys" {
		t.Fatalf("query = %q, want sys", model.query)
	}
}

func TestTruncateKeepsUTF8(t *testing.T) {
	got := truncate("函数调用路径", 5)
	if got != "函数..." {
		t.Fatalf("truncate() = %q, want 函数...", got)
	}
}

// TestSearchRefreshesAfterZoomOut verifies a zoom-out re-runs the active
// search so matches in newly visible sibling branches are discoverable.
func TestSearchRefreshesAfterZoomOut(t *testing.T) {
	frames := []flamegraph.FrameData{
		{Level: 0, Value: 100, Self: 0, Label: "root"},
		{Level: 1, Value: 50, Self: 0, Label: "request-a"},
		{Level: 2, Value: 50, Self: 50, Label: "handle-a"},
		{Level: 1, Value: 50, Self: 0, Label: "request-b"},
		{Level: 2, Value: 50, Self: 50, Label: "handle-b"},
	}
	model := NewModel(frames)
	model.width = 80
	model.height = 20

	// Zoom into request-a and search for "handle".
	model, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyDown})
	model, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	if model.focus.Label != "request-a" {
		t.Fatalf("focus = %q, want request-a", model.focus.Label)
	}
	model.query = "handle"
	model.applySearch()
	if len(model.matches) != 1 {
		t.Fatalf("matches while zoomed = %d, want 1", len(model.matches))
	}

	// Zoom out: handle-b is now visible and must be discovered.
	model, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if model.focus.Label != "root" {
		t.Fatalf("focus after zoom out = %q, want root", model.focus.Label)
	}
	if len(model.matches) != 2 {
		t.Fatalf("matches after zoom out = %d, want 2", len(model.matches))
	}

	// n navigates to the second distinct match.
	first := model.selectedNode()
	model.nextMatch()
	second := model.selectedNode()
	if second == first {
		t.Fatalf("nextMatch stayed on %v; matches not navigable after zoom out", first.Label)
	}
}

func TestFrameColumnLargeValues(t *testing.T) {
	tests := []struct {
		name  string
		value int64
		total int64
		want  int
	}{
		{
			name:  "full maximum",
			value: math.MaxInt64,
			total: math.MaxInt64,
			want:  100,
		},
		{
			name:  "just below maximum",
			value: math.MaxInt64 - 1,
			total: math.MaxInt64,
			want:  99,
		},
		{
			name:  "large half",
			value: 1 << 59,
			total: 1 << 60,
			want:  50,
		},
		{
			name:  "large three quarters",
			value: 3 << 58,
			total: 1 << 60,
			want:  75,
		},
		{
			name:  "above total",
			value: math.MaxInt64,
			total: 1,
			want:  100,
		},
		{
			name:  "subcolumn positive weight",
			value: 1,
			total: 1000,
			want:  0,
		},
		{
			name:  "zero value",
			value: 0,
			total: 100,
			want:  0,
		},
		{
			name:  "quarter value",
			value: 25,
			total: 100,
			want:  25,
		},
		{
			name:  "zero total",
			value: 100,
			total: 0,
			want:  0,
		},
		{
			name:  "negative value",
			value: -1,
			total: 100,
			want:  0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := frameColumn(tc.value, tc.total, 100); got != tc.want {
				t.Fatalf("frameColumn(%d, %d, 100) = %d, want %d", tc.value, tc.total, got, tc.want)
			}
		})
	}
}

func TestStackTreeKeepsBothThreeLevelBranches(t *testing.T) {
	root, err := buildTestStackTree(t.Context(), map[string]int64{
		"main;Dep_1_A;Dep_2_A;Dep_3_A": 70,
		"main;Dep_1_B;Dep_2_B;Dep_3_B": 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if root.Label != "main" || root.Value != 100 || root.Self != 0 {
		t.Fatalf("root = %+v", root)
	}
	for _, branch := range []string{"A", "B"} {
		node := root
		for _, name := range []string{"Dep_1_" + branch, "Dep_2_" + branch, "Dep_3_" + branch} {
			var found *Node
			for _, child := range node.Children {
				if child.Label == name {
					found = child
					break
				}
			}
			if found == nil || found.Parent != node {
				t.Fatalf("missing %s below %s", name, node.Label)
			}
			node = found
		}
		if node.Self != node.Value {
			t.Fatalf("leaf %s has self=%d total=%d", node.Label, node.Self, node.Value)
		}
	}
}

func TestStackTreeRejectsInvalidWeights(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stacks map[string]int64
	}{
		{
			name:   "negative weight",
			stacks: map[string]int64{"main": -1},
		},
		{
			name:   "missing frames",
			stacks: map[string]int64{"": 1},
		},
		{
			name:   "total overflow",
			stacks: map[string]int64{"a": math.MaxInt64, "b": 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := buildTestStackTree(t.Context(), tc.stacks); err == nil {
				t.Fatalf("accepted invalid stacks: %v", tc.stacks)
			}
		})
	}
	root, err := buildTestStackTree(t.Context(), map[string]int64{"main": 0})
	if err != nil || root.Value != 0 || root.Label != "empty" {
		t.Fatalf("empty tree=%+v, error=%v", root, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewStackTree(ctx).BuildModel(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestHorizontalSelectionAcrossDifferentParents(t *testing.T) {
	root, err := buildTestStackTree(t.Context(), map[string]int64{"main;a;a2;a3": 70, "main;b;b2;b3": 30})
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(root, "test")
	m.query = "a2"
	m.applySearch()
	m, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyRight})
	if m.selectedNode().Label != "b2" || m.focus != root {
		t.Fatalf("right selected=%s focus=%s", m.selectedNode().Label, m.focus.Label)
	}
	m, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyRight})
	if m.selectedNode().Label != "b2" {
		t.Fatal("right wrapped")
	}
	m, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyLeft})
	if m.selectedNode().Label != "a2" {
		t.Fatal("left did not return to a2")
	}
	m, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyLeft})
	if m.selectedNode().Label != "a2" {
		t.Fatal("left wrapped")
	}
	m, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.focus.Label != "a2" {
		t.Fatal("enter did not zoom")
	}
	m, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.focus.Label != "a" {
		t.Fatal("backspace did not zoom out")
	}
}

func TestHorizontalViewGeometryAndTerminalBounds(t *testing.T) {
	root, err := buildTestStackTree(t.Context(), map[string]int64{"main;left;child": 50, "main;left": 20, "main;right": 30})
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(root, "test")
	m.width = 100
	m.height = 20
	lines := strings.Split(ansi.Strip(m.View()), "\n")
	if strings.Index(lines[5], "right") != 70 {
		t.Fatalf("siblings not proportional: %q", lines[5])
	}
	if strings.Index(lines[6], "child") != 20 {
		t.Fatalf("self time not preserved: %q", lines[6])
	}
	for _, size := range []struct {
		name   string
		width  int
		height int
	}{
		{
			name:   "single cell",
			width:  1,
			height: 1,
		},
		{
			name:   "short terminal",
			width:  12,
			height: 8,
		},
		{
			name:   "one graph row",
			width:  40,
			height: 10,
		},
		{
			name:   "regular terminal",
			width:  80,
			height: 20,
		},
	} {
		t.Run(size.name, func(t *testing.T) {
			m.width = size.width
			m.height = size.height
			lines = strings.Split(strings.TrimSuffix(m.View(), "\n"), "\n")
			if len(lines) > size.height {
				t.Fatalf("rows=%d height=%d", len(lines), size.height)
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > size.width {
					t.Fatalf("width exceeded: %q", line)
				}
			}
		})
	}
}

func TestStackTreePreservesOtherFunction(t *testing.T) {
	root, err := buildTestStackTree(t.Context(), map[string]int64{"main;other;leaf": 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Children) != 1 || root.Children[0].Label != "other" || len(root.Children[0].Children) != 1 || root.Children[0].Children[0].Label != "leaf" {
		t.Fatalf("other function lost its child: %+v", root)
	}
}

// Tiny positive frames cannot all fit a proportional graph, but must remain
// discoverable and selectable through the advertised all-frames view.
func TestAllFramesViewPreservesTinyNodes(t *testing.T) {
	stacks := map[string]int64{"r;h": 900}
	for i := range 100 {
		stacks[fmt.Sprintf("r;b%02x;t%02x", i, i)] = 1
	}
	root, err := buildTestStackTree(t.Context(), stacks)
	if err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{12, 40, 100} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			m := newModel(root, "test")
			m.width = width
			m.height = 20
			_, shown := m.renderFlamegraph()
			if len(m.visible) != 202 || shown >= len(m.visible) {
				t.Fatalf("width %d: visible=%d shown=%d", width, len(m.visible), shown)
			}
			if width == 100 && !strings.Contains(m.View(), "ALL frames, including tiny") {
				t.Fatal("graph does not advertise the tiny-frame entry")
			}
			updated, _ := m.Update(tea.KeyMsg{
				Type:  tea.KeyRunes,
				Runes: []rune{'v'},
			})
			m = updated.(Model)
			if !m.isListView || m.focus != root || m.cursor != 0 {
				t.Fatal("view toggle changed the selection or focus")
			}
			for index, node := range m.visible {
				if index > 0 {
					updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
					m = updated.(Model)
				}
				if m.selectedNode() != node {
					t.Fatalf("width %d: cannot select frame %d", width, index)
				}
				lines := strings.Split(ansi.Strip(m.View()), "\n")
				selected := ""
				for _, line := range lines[headerLines : headerLines+m.viewportHeight()] {
					if strings.HasPrefix(line, "> ") {
						selected = line
					}
					if ansi.StringWidth(line) > width {
						t.Fatalf("list row exceeds width %d: %q", width, line)
					}
				}
				if !strings.Contains(selected, "> "+node.Label) {
					t.Fatalf("width %d: selected frame %q is not visible: %q", width, node.Label, selected)
				}
				if node.Value == 1 && !strings.Contains(selected, "0.10%") {
					t.Fatalf("tiny frame lost its weight: %q", selected)
				}
			}
		})
	}
}

func TestAllFramesViewKeepsSearchNavigationAndZoom(t *testing.T) {
	root, err := buildTestStackTree(t.Context(), map[string]int64{"r;a;x": 999, "r;b;y": 1})
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(root, "test")
	m, _ = m.updateKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'v'},
	})
	m.query = "y"
	m.applySearch()
	if !strings.Contains(ansi.Strip(m.View()), "> y") {
		t.Fatal("search selected an invisible tiny frame")
	}
	m, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyLeft})
	if m.selectedNode().Label != "x" || m.focus != root {
		t.Fatal("list navigation lost the current depth or focus")
	}
	m.query = "b"
	m.applySearch()
	m, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.focus.Label != "b" || !strings.Contains(ansi.Strip(m.View()), "y") {
		t.Fatal("zoom did not preserve the tiny subtree")
	}
	m, _ = m.updateKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.focus != root || m.selectedNode().Label != "b" || !m.isListView {
		t.Fatal("zoom out lost selection or list mode")
	}
	cursor := m.cursor
	m, _ = m.updateKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'v'},
	})
	if m.isListView || m.cursor != cursor || m.focus != root {
		t.Fatal("returning to the graph changed selection or focus")
	}
}

// Cancel a real context after work starts, independently of wall-clock scheduling.
type cancelDuringPreparation struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancelDuringPreparation) Err() error {
	c.checks--
	if c.checks == 0 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestStackPreparationObservesCancellationDuringWork(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	workCtx := &cancelDuringPreparation{
		Context: ctx,
		cancel:  cancel,
		checks:  5,
	}
	if err := NewStackTree(workCtx).Add([]string{"main", "a", "b", "c"}, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("preparation ignored cancellation during work: %v", err)
	}
}

func TestViewEscapesFrameNames(t *testing.T) {
	rootName := "根\x1b[2J\n"
	childName := "子\x1b]0;bad\a\r\n"
	model := NewModel([]flamegraph.FrameData{
		{
			Label: rootName,
			Value: 100,
		},
		{
			Label: childName,
			Level: 1,
			Value: 100,
		},
	})
	model.title = "title\x1b[2J"
	model.query = "子"
	model.applySearch()
	selected := model.selectedNode()
	model.query = "query\x1b[2J\n"
	model.lastSearch = model.query
	model.height = 20
	for _, width := range []int{40, 100} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			model.width = width
			for _, list := range []bool{false, true} {
				model.isListView = list
				for _, searching := range []bool{false, true} {
					model.searching = searching
					view := ansi.Strip(model.View())
					if strings.ContainsAny(view, "\x1b\a\r") {
						t.Fatalf("terminal control characters escaped styling: %q", view)
					}
					lines := strings.Split(strings.TrimSuffix(view, "\n"), "\n")
					if len(lines) > model.height {
						t.Fatalf("frame names injected rows: %d", len(lines))
					}
					for _, line := range lines {
						if ansi.StringWidth(line) > width {
							t.Fatalf("escaped row exceeds width %d: %q", width, line)
						}
					}
					if width == 100 {
						for _, text := range []string{`title\x1b[2J`, `focus=根\x1b[2J\n`, `query\x1b[2J\n`, `selected: 子\x1b]0;bad\a\r\n`, `path: 根\x1b[2J\n > 子\x1b]0;bad\a\r\n`} {
							if !strings.Contains(view, text) {
								t.Fatalf("missing escaped text %q: %q", text, view)
							}
						}
						marker := ">"
						if list {
							marker = "> "
						}
						if !strings.Contains(lines[headerLines+1], marker+`子\x1b]0;bad\a\r\n`) {
							t.Fatalf("selected frame lost its visible marker: %q", lines[headerLines+1])
						}
					}
					if model.selectedNode() != selected || model.focus.Label != rootName || selected.Label != childName {
						t.Fatal("rendering changed the original frame names or selection")
					}
				}
			}
		})
	}
}

func TestTerminalTextEscapesControls(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{
			name: "wide characters",
			text: "函数",
			want: "函数",
		},
		{
			name: "c0 and delete",
			text: "\x1b\n\r\t\x00\x7f",
			want: `\x1b\n\r\t\x00\x7f`,
		},
		{
			name: "c1",
			text: "\u009b",
			want: `\u009b`,
		},
		{
			name: "osc",
			text: "\x1b]52;c;payload\a",
			want: `\x1b]52;c;payload\a`,
		},
		{
			name: "bidirectional control",
			text: "x\u202ey",
			want: `x\u202ey`,
		},
		{
			name: "invalid utf8",
			text: "\xff",
			want: `\xff`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminalText(tc.text); got != tc.want {
				t.Fatalf("terminal text = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSearchKeepsOriginalControlCharacters(t *testing.T) {
	model := NewModel([]flamegraph.FrameData{{Label: "frame\x1b[2J"}})
	model.query = "\x1b[2J"
	model.applySearch()
	if !model.matches[model.focus] || model.lastSearch != model.query {
		t.Fatal("search compared escaped names instead of original names")
	}
	model = model.updateSearch(tea.KeyMsg{Type: tea.KeyBackspace})
	if model.query != "\x1b[2" {
		t.Fatalf("backspace changed raw query handling: %q", model.query)
	}
}

// buildTestStackTree keeps compact path fixtures out of the structured API.
func buildTestStackTree(ctx context.Context, paths map[string]int64) (*Node, error) {
	tree := NewStackTree(ctx)
	for path, weight := range paths {
		var frames []string
		if path != "" {
			frames = strings.Split(path, ";")
		}
		if err := tree.Add(frames, weight); err != nil {
			return nil, err
		}
	}
	model, err := tree.BuildModel()
	if err != nil {
		return nil, err
	}
	if model == nil {
		return BuildTree(nil), nil
	}
	return model.focus, nil
}

func TestStackTreePreservesDistinctRawFrameNames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		left  string
		right string
	}{
		{
			name:  "semicolon and colon",
			left:  "foo;bar",
			right: "foo:bar",
		},
		{
			name:  "newline and space",
			left:  "foo\nbar",
			right: "foo bar",
		},
		{
			name:  "carriage return and space",
			left:  "foo\rbar",
			right: "foo bar",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tree := NewStackTree(t.Context())
			frames := []string{"main", tc.left}
			if err := tree.Add(frames, 10); err != nil {
				t.Fatal(err)
			}
			frames[1] = tc.right
			if err := tree.Add(frames, 10); err != nil {
				t.Fatal(err)
			}
			model, err := tree.BuildModel()
			if err != nil {
				t.Fatal(err)
			}
			root := model.focus
			if root.Label != "main" || root.Value != 20 || root.Self != 0 || len(root.Children) != 2 {
				t.Fatalf("distinct stacks merged or lost their root: %+v", root)
			}
			names := map[string]bool{tc.left: false, tc.right: false}
			for _, child := range root.Children {
				if _, ok := names[child.Label]; !ok || names[child.Label] {
					t.Fatalf("unexpected or duplicate raw frame %q", child.Label)
				}
				names[child.Label] = true
				if child.Parent != root || child.Value != 10 || child.Self != 10 || len(child.Children) != 0 || percent(child.Value, root.Value) != 50 {
					t.Fatalf("raw frame lost its independent weight: %+v", child)
				}
			}
		})
	}
}

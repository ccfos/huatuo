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
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	tea "github.com/charmbracelet/bubbletea"
)

func TestStackTreeWithMultipleRoots(t *testing.T) {
	model := newTestModel(t, map[string]int64{
		"pid-a":        10,
		"pid-a;work-a": 50,
		"pid-b":        15,
		"pid-b;work-b": 25,
	})
	root := model.visible.root()
	if root.Label != "total" {
		t.Fatalf("root label = %q, want total", root.Label)
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

func TestModelViewIncludesDetails(t *testing.T) {
	model := newTestModel(t, map[string]int64{"root;runtime.main": 100})
	model.width = 80
	model.height = 20
	view := model.View()
	for _, want := range []string{"HUATUO tui flamegraph", "runtime.main", "selected:", "path:"} {
		if !strings.Contains(view, want) {
			t.Fatalf("View() missing %q in:\n%s", want, view)
		}
	}
}

func TestSearchInputUsesRunesOnly(t *testing.T) {
	var model Model
	model.searching = true
	model.updateSearch(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("sys")})
	model.updateSearch(tea.KeyMsg{Type: tea.KeyCtrlA})
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
	model := newTestModel(t, map[string]int64{
		"root;request-a;handle-a": 50,
		"root;request-b;handle-b": 50,
	})
	model.width = 80
	model.height = 20

	model.query = "request-a"
	model.applySearch()
	if got := model.selectedNode().Label; got != "request-a" {
		t.Fatalf("search selected %q, want request-a", got)
	}
	model.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	if model.visible.root().Label != "request-a" {
		t.Fatalf("focus = %q, want request-a", model.visible.root().Label)
	}
	model.query = "handle"
	model.applySearch()
	if len(model.matches) != 1 {
		t.Fatalf("matches while zoomed = %d, want 1", len(model.matches))
	}

	// Zoom out: handle-b is now visible and must be discovered.
	model.updateKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if model.visible.root().Label != "root" {
		t.Fatalf("focus after zoom out = %q, want root", model.visible.root().Label)
	}
	if len(model.matches) != 2 {
		t.Fatalf("matches after zoom out = %d, want 2", len(model.matches))
	}

	for _, want := range []string{"handle-a", "handle-b"} {
		model.updateKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
		if got := model.selectedNode().Label; got != want {
			t.Fatalf("next match after zoom out = %q, want %q", got, want)
		}
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

func TestStackTreeAggregatesSharedPrefixes(t *testing.T) {
	model := newTestModel(t, map[string]int64{
		"main;left;work;leaf":  40,
		"main;left;work":       30,
		"main;other;work;leaf": 30,
	})
	root := model.visible.root()
	if root.Label != "main" || root.Value != 100 || root.Self != 0 || root.Parent != nil || len(root.Children) != 2 {
		t.Fatalf("unexpected root: %+v", root)
	}

	for i, want := range []struct {
		label string
		total int64
		self  int64
	}{
		{
			label: "left",
			total: 70,
			self:  30,
		},
		{
			label: "other",
			total: 30,
		},
	} {
		branch := root.Children[i]
		if branch.Label != want.label || branch.Value != want.total || branch.Self != 0 || branch.Parent != root || len(branch.Children) != 1 {
			t.Fatalf("unexpected branch %q: %+v", want.label, branch)
		}
		work := branch.Children[0]
		if work.Label != "work" || work.Value != want.total || work.Self != want.self || work.Parent != branch || len(work.Children) != 1 {
			t.Fatalf("unexpected work below %q: %+v", want.label, work)
		}
		leaf := work.Children[0]
		if weight := want.total - want.self; leaf.Label != "leaf" || leaf.Value != weight || leaf.Self != weight || leaf.Parent != work || len(leaf.Children) != 0 {
			t.Fatalf("unexpected leaf below %q: %+v", want.label, leaf)
		}
	}
}

func TestStackTreeRejectsInvalidWeights(t *testing.T) {
	tree := NewStackTree(t.Context())
	if err := tree.Add([]string{"main"}, math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	if err := tree.Add([]string{"leaf"}, 1); err == nil {
		t.Fatal("accepted a total above MaxInt64")
	}
	if err := NewStackTree(t.Context()).Add([]string{"main"}, -1); err == nil {
		t.Fatal("accepted a negative weight")
	}
	if err := NewStackTree(t.Context()).Add(nil, 1); err == nil {
		t.Fatal("accepted positive weight without frames")
	}
}

func TestStackTreeBuildModelWithoutSamples(t *testing.T) {
	tree := NewStackTree(t.Context())
	if err := tree.Add([]string{"main"}, 0); err != nil {
		t.Fatal(err)
	}
	if model, err := tree.BuildModel(); err != nil || model != nil {
		t.Fatalf("zero weight: model=%p, error=%v", model, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := NewStackTree(ctx).BuildModel(); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v, want context.Canceled", err)
	}
}

func TestGraphNavigationFollowsTree(t *testing.T) {
	m := newTestModel(t, map[string]int64{
		"root;a;a1;leaf": 40,
		"root;a;a2":      20,
		"root;b;b1":      40,
	})
	pressAndExpect := func(key tea.KeyType, want string) {
		t.Helper()
		updated, _ := m.Update(tea.KeyMsg{Type: key})
		m = updated.(Model)
		if got := m.selectedNode().Label; got != want {
			t.Fatalf("after %s, selected %q, want %q", key, got, want)
		}
	}

	pressAndExpect(tea.KeyUp, "root")
	pressAndExpect(tea.KeyDown, "a")
	pressAndExpect(tea.KeyRight, "b")
	pressAndExpect(tea.KeyDown, "b1")
	pressAndExpect(tea.KeyLeft, "a2")
	pressAndExpect(tea.KeyUp, "a")
	pressAndExpect(tea.KeyDown, "a1")
	pressAndExpect(tea.KeyDown, "leaf")
	pressAndExpect(tea.KeyDown, "root")

	pressAndExpect(tea.KeyDown, "a")
	pressAndExpect(tea.KeyEnter, "a")
	pressAndExpect(tea.KeyUp, "a")
	pressAndExpect(tea.KeyDown, "a1")
	pressAndExpect(tea.KeyDown, "leaf")
	pressAndExpect(tea.KeyDown, "a")
}

func TestHorizontalSelectionAcrossDifferentParents(t *testing.T) {
	m := newTestModel(t, map[string]int64{"main;a;a2;a3": 70, "main;b;b2;b3": 30})
	root := m.visible.root()
	m.query = "a2"
	m.applySearch()
	m.updateKey(tea.KeyMsg{Type: tea.KeyRight})
	if m.selectedNode().Label != "b2" || m.visible.root() != root {
		t.Fatalf("right selected=%s focus=%s", m.selectedNode().Label, m.visible.root().Label)
	}
	m.updateKey(tea.KeyMsg{Type: tea.KeyRight})
	if m.selectedNode().Label != "b2" {
		t.Fatal("right wrapped")
	}
	m.updateKey(tea.KeyMsg{Type: tea.KeyLeft})
	if m.selectedNode().Label != "a2" {
		t.Fatal("left did not return to a2")
	}
	m.updateKey(tea.KeyMsg{Type: tea.KeyLeft})
	if m.selectedNode().Label != "a2" {
		t.Fatal("left wrapped")
	}
	m.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.visible.root().Label != "a2" {
		t.Fatal("enter did not zoom")
	}
	m.updateKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.visible.root().Label != "a" {
		t.Fatal("backspace did not zoom out")
	}
}

func TestHorizontalViewGeometryAndTerminalBounds(t *testing.T) {
	m := newTestModel(t, map[string]int64{"main;left;child": 50, "main;left": 20, "main;right": 30})
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
			viewLines(t, m.View(), m.width, m.height)
		})
	}
}

// Tiny positive frames cannot all fit a proportional graph, but must remain
// discoverable and selectable through the advertised all-frames view.
func TestAllFramesViewPreservesTinyNodes(t *testing.T) {
	const branches = 12
	stacks := map[string]int64{"r;hot": 1000 - branches}
	for i := range branches {
		stacks[fmt.Sprintf("r;b%02d;t%02d", i, i)] = 1
	}
	model := newTestModel(t, stacks)
	t.Run("graph cursor", func(t *testing.T) {
		m := model
		m.width = 40
		m.height = 20
		for i, label := range []string{"b00", "t00"} {
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
			m = updated.(Model)
			lines := viewLines(t, m.View(), m.width, m.height)
			row := headerLines + 1 + i
			if !strings.HasPrefix(lines[row], ">") {
				t.Fatalf("selected frame %q has no cursor on graph row %d: %q", label, row, lines[row])
			}
			view := strings.Join(lines, "\n")
			if !strings.Contains(view, "selected: "+label+"\n") ||
				!strings.Contains(view, "value: 1  self: ") || !strings.Contains(view, "focus: 0.10%") {
				t.Fatalf("selected frame %q lost its details:\n%s", label, view)
			}
		}
	})

	for _, width := range []int{12, 40, 100} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			m := model
			m.width = width
			m.height = 20
			_, shown := m.renderFlamegraph()
			if shown >= 2+2*branches {
				t.Fatalf("fixture did not produce any subcolumn frames: shown=%d", shown)
			}
			if width == 100 && !strings.Contains(m.View(), "ALL frames, including tiny") {
				t.Fatal("graph does not advertise the tiny-frame entry")
			}
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
			m = updated.(Model)
			want := []string{"r"}
			for i := range branches {
				want = append(want, fmt.Sprintf("b%02d", i), fmt.Sprintf("t%02d", i))
			}
			want = append(want, "hot")
			for i, label := range want {
				if i > 0 {
					updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
					m = updated.(Model)
				}
				lines := viewLines(t, m.View(), m.width, m.height)
				var selected string
				for _, line := range lines {
					if strings.HasPrefix(line, "> ") {
						selected = line
						break
					}
				}
				if !strings.HasPrefix(selected, "> "+label+" ") {
					t.Fatalf("expected selected frame %q at step %d, got %q", label, i, selected)
				}
				if i > 0 && i < len(want)-1 && !strings.HasSuffix(selected, "0.10%") {
					t.Fatalf("tiny frame lost its weight: %q", selected)
				}
			}
		})
	}
}

func TestOverlayCursorPreservesColumnAlignment(t *testing.T) {
	line := "\x1b[45m界xy\x1b[0m"
	got := ansi.Strip(overlayCursor(line, 1, ">"))
	if want := " >xy"; got != want {
		t.Fatalf("overlaid row = %q, want %q", got, want)
	}
}

func TestAllFramesViewKeepsSearchNavigationAndZoom(t *testing.T) {
	m := newTestModel(t, map[string]int64{"r;a;x": 999, "r;b;y": 1})
	root := m.visible.root()
	m.updateKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'v'},
	})
	m.query = "y"
	m.applySearch()
	if !strings.Contains(ansi.Strip(m.View()), "> y") {
		t.Fatal("search selected an invisible tiny frame")
	}
	m.updateKey(tea.KeyMsg{Type: tea.KeyLeft})
	if m.selectedNode().Label != "x" || m.visible.root() != root {
		t.Fatal("list navigation lost the current depth or focus")
	}
	m.query = "b"
	m.applySearch()
	m.updateKey(tea.KeyMsg{Type: tea.KeyEnter})
	if m.visible.root().Label != "b" || !strings.Contains(ansi.Strip(m.View()), "y") {
		t.Fatal("zoom did not preserve the tiny subtree")
	}
	m.updateKey(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.visible.root() != root || m.selectedNode().Label != "b" || m.view != viewFrames {
		t.Fatal("zoom out lost selection or list mode")
	}
	cursor := m.cursor
	m.updateKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'v'},
	})
	if m.view != viewGraph || m.cursor != cursor || m.visible.root() != root {
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

func TestGroupingReorderReusesFunctionNodes(t *testing.T) {
	for _, tt := range []struct {
		name   string
		cancel bool
	}{
		{name: "adopt reordered groups"},
		{name: "cancel during reorder", cancel: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			model := newTestModel(t, map[string]int64{
				"cpu0;comm=api;request": 2,
				"cpu0;comm=worker;work": 3,
			})
			originalRoot := model.tree.root()
			originalParent := originalRoot.Children[0]
			function := originalParent.Children[0]
			model.focusOn(originalParent)

			ctx := t.Context()
			if tt.cancel {
				workCtx, cancel := context.WithCancel(ctx)
				defer cancel()
				ctx = &cancelDuringPreparation{
					Context: workCtx,
					cancel:  cancel,
					checks:  4,
				}
			}
			model.grouping = &groupingState{
				ctx:   ctx,
				tags:  []string{"cpu", "comm"},
				input: "comm,cpu",
				buildTree: func(context.Context, []string) (*StackTree, error) {
					t.Fatal("changing only label order must reuse function nodes")
					return nil, nil
				},
			}

			cmd := model.submitGrouping()
			if cmd == nil {
				t.Fatal("reorder did not start")
			}
			result := cmd().(groupingResult)
			if function.Parent != originalParent {
				t.Fatal("preparing the reorder mutated the active tree")
			}

			model.applyGrouping(&result)
			if tt.cancel {
				if !errors.Is(result.err, context.Canceled) || model.tree.root() != originalRoot ||
					model.visible.root() != originalParent || function.Parent != originalParent ||
					!slices.Equal(model.grouping.tags, []string{"cpu", "comm"}) {
					t.Fatalf("canceled reorder changed the active grouping: %v", result.err)
				}
				return
			}

			if result.err != nil {
				t.Fatal(result.err)
			}
			root := model.tree.root()
			if root.Value != 5 || model.visible.root() != root || !slices.Equal(model.grouping.tags, []string{"comm", "cpu"}) {
				t.Fatalf("reordered root weight = %d, grouping = %q", root.Value, model.grouping.tags)
			}
			if got := nodePath(function); got != "total > comm=api > cpu0 > request" {
				t.Fatalf("reused function path = %q", got)
			}
			if function.Value != 2 || function.Self != 2 || model.tree[3] != function {
				t.Fatal("reordered view lost the original function node or its weight")
			}

			model.grouping.input = "cpu,comm"
			cmd = model.submitGrouping()
			if cmd == nil {
				t.Fatal("reverse reorder did not start")
			}
			result = cmd().(groupingResult)
			model.applyGrouping(&result)
			if got := nodePath(function); got != "cpu0 > comm=api > request" || model.tree.root().Value != 5 {
				t.Fatalf("restored function path = %q, root weight = %d", got, model.tree.root().Value)
			}
		})
	}
}

func TestViewEscapesFrameNames(t *testing.T) {
	rootName := "根\x1b[2J\n"
	childName := "子\x1b]0;bad\a\r\n"
	tree := NewStackTree(t.Context())
	if err := tree.Add([]string{rootName, childName}, 100); err != nil {
		t.Fatal(err)
	}
	model, err := tree.BuildModel()
	if err != nil || model == nil {
		t.Fatalf("build fixture: model=%p, error=%v", model, err)
	}
	model.query = "子"
	model.applySearch()
	selected := model.selectedNode()
	model.query = "query\x1b[2J\n"
	model.lastSearch = model.query
	model.height = 20
	for _, width := range []int{40, 100} {
		t.Run(fmt.Sprintf("width_%d", width), func(t *testing.T) {
			model.width = width
			for _, mode := range []viewMode{viewGraph, viewFrames} {
				model.view = mode
				for _, searching := range []bool{false, true} {
					model.searching = searching
					view := model.View()
					if strings.Contains(view, "\x1b[2J") || strings.Contains(view, "\x1b]0;bad") || strings.ContainsAny(view, "\a\r") {
						t.Fatalf("view contains unescaped input: %q", view)
					}
					lines := viewLines(t, view, width, model.height)
					if width == 100 {
						plain := strings.Join(lines, "\n")
						for _, text := range []string{`focus=根\x1b[2J\n`, `query\x1b[2J\n`, `selected: 子\x1b]0;bad\a\r\n`, `path: 根\x1b[2J\n > 子\x1b]0;bad\a\r\n`} {
							if !strings.Contains(plain, text) {
								t.Fatalf("missing escaped text %q: %q", text, view)
							}
						}
						marker := ">"
						if mode == viewFrames {
							marker = "> "
						}
						if !strings.Contains(lines[headerLines+1], marker+`子\x1b]0;bad\a\r\n`) {
							t.Fatalf("selected frame lost its visible marker: %q", lines[headerLines+1])
						}
					}
					if model.selectedNode() != selected || model.visible.root().Label != rootName || selected.Label != childName {
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
	model := newTestModel(t, map[string]int64{"frame\x1b[2J": 1})
	model.query = "\x1b[2J"
	model.applySearch()
	if !model.matches[model.visible.root()] || model.lastSearch != model.query {
		t.Fatal("search compared escaped names instead of original names")
	}
	model.updateSearch(tea.KeyMsg{Type: tea.KeyBackspace})
	if model.query != "\x1b[2" {
		t.Fatalf("backspace changed raw query handling: %q", model.query)
	}
}

// newTestModel uses semicolon-separated fixtures for viewer tests with simple names.
func newTestModel(t *testing.T, paths map[string]int64) Model {
	t.Helper()

	tree := NewStackTree(t.Context())
	for path, weight := range paths {
		if err := tree.Add(strings.Split(path, ";"), weight); err != nil {
			t.Fatal(err)
		}
	}
	model, err := tree.BuildModel()
	if err != nil || model == nil {
		t.Fatalf("build fixture: model=%p, error=%v", model, err)
	}

	return *model
}

func viewLines(t *testing.T, view string, width, height int) []string {
	t.Helper()

	lines := strings.Split(strings.TrimSuffix(ansi.Strip(view), "\n"), "\n")
	if len(lines) > height {
		t.Fatalf("view has %d rows in a %d-row terminal", len(lines), height)
	}
	for _, line := range lines {
		if ansi.StringWidth(line) > width {
			t.Fatalf("view exceeds width %d: %q", width, line)
		}
	}

	return lines
}

func TestStackTreePreservesDistinctRawFrameNames(t *testing.T) {
	names := []string{"foo\nbar", "foo\rbar", "foo bar", "foo:bar", "foo;bar"}
	tree := NewStackTree(t.Context())
	for i := len(names) - 1; i >= 0; i-- {
		if err := tree.Add([]string{"main", names[i]}, int64(i+1)); err != nil {
			t.Fatal(err)
		}
	}

	model, err := tree.BuildModel()
	if err != nil || model == nil {
		t.Fatalf("build raw frames: model=%p, error=%v", model, err)
	}
	root := model.visible.root()
	if root.Label != "main" || root.Value != 15 || root.Self != 0 || len(root.Children) != len(names) {
		t.Fatalf("distinct stacks merged or lost their root: %+v", root)
	}
	for i, child := range root.Children {
		if weight := int64(i + 1); child.Label != names[i] || child.Value != weight || child.Self != weight {
			t.Fatalf("raw frame %q lost its identity or weight: %+v", names[i], child)
		}
	}
}

func TestStackTreeTransfersNodesToModel(t *testing.T) {
	tree := NewStackTree(t.Context())
	if err := tree.Add([]string{"main", "work"}, 2); err != nil {
		t.Fatal(err)
	}
	model, err := tree.BuildModel()
	if err != nil || model == nil {
		t.Fatalf("initial build: model=%p, err=%v", model, err)
	}

	if err := tree.Add([]string{"main", "work"}, 5); err == nil {
		t.Fatal("add accepted after transferring nodes to the model")
	}
	if again, err := tree.BuildModel(); again != nil || err == nil {
		t.Fatalf("repeated build: model=%p, err=%v", again, err)
	}
	if model.visible.root().Value != 2 || len(model.visible.root().Children) != 1 || model.visible.root().Children[0].Self != 2 {
		t.Fatalf("builder changed the transferred tree: %+v", model.visible.root())
	}
}

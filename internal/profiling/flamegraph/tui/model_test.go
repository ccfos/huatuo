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
	"math"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestStackTreeWeightsAndOwnership(t *testing.T) {
	tree := NewStackTree(t.Context())
	require.NoError(t, tree.Add([]string{"a", "work"}, 30))
	require.NoError(t, tree.Add([]string{"a", "work", "leaf"}, 40))
	require.NoError(t, tree.Add([]string{"b", "work"}, 30))
	model := buildTestModel(t, tree)
	require.Len(t, model.visible, 6)
	got := make(map[string][2]int64)
	for _, node := range model.visible {
		got[nodePath(node)] = [2]int64{node.Value, node.Self}
	}
	require.Equal(t, map[string][2]int64{
		"total":                   {100, 0},
		"total > a":               {70, 0},
		"total > a > work":        {70, 30},
		"total > a > work > leaf": {40, 40},
		"total > b":               {30, 0},
		"total > b > work":        {30, 30},
	}, got)

	require.Error(t, tree.Add([]string{"a"}, 1))
	again, err := tree.BuildModel()
	require.Error(t, err)
	require.Nil(t, again)
	require.EqualValues(t, 100, model.visible.root().Value)
}

func TestStackTreeWeightLimits(t *testing.T) {
	tree := NewStackTree(t.Context())
	require.NoError(t, tree.Add([]string{"root"}, 0))
	model, err := tree.BuildModel()
	require.NoError(t, err)
	require.Nil(t, model)

	tree = NewStackTree(t.Context())
	require.NoError(t, tree.Add([]string{"root"}, math.MaxInt64))
	require.ErrorContains(t, tree.Add([]string{"root"}, 1), "exceeds int64")
	require.ErrorContains(t, NewStackTree(t.Context()).Add([]string{"root"}, -1), "negative weight")
	require.ErrorContains(t, NewStackTree(t.Context()).Add(nil, 1), "has no frames")
}

// Cancel at a context check so the test does not depend on a wall-clock delay.
type cancelDuringWork struct {
	context.Context
	cancel context.CancelFunc
	checks int
}

func (c *cancelDuringWork) Err() error {
	c.checks--
	if c.checks == 0 {
		c.cancel()
	}

	return c.Context.Err()
}

func TestStackTreeCancellation(t *testing.T) {
	ctx := cancelOnCheck(t, 3)
	require.ErrorIs(t, NewStackTree(ctx).Add([]string{"root", "a", "b"}, 1), context.Canceled)
	_, err := NewStackTree(ctx).BuildModel()
	require.ErrorIs(t, err, context.Canceled)
}

func TestGraphNavigation(t *testing.T) {
	m := testModel(t, map[string]int64{"root;a;x": 70, "root;b;y": 30})
	step := func(key tea.KeyType, selected, focus string) {
		t.Helper()
		pressKey(&m, key, "")
		if got := m.selectedNode().Label; got != selected || m.visible.root().Label != focus {
			t.Fatalf("%s: selected=%q focus=%q, want %q and %q", key, got, m.visible.root().Label, selected, focus)
		}
	}
	step(tea.KeyUp, "root", "root")
	step(tea.KeyDown, "a", "root")
	step(tea.KeyDown, "x", "root")
	step(tea.KeyLeft, "x", "root")
	step(tea.KeyRight, "y", "root")
	step(tea.KeyRight, "y", "root")
	step(tea.KeyUp, "b", "root")
	step(tea.KeyEnter, "b", "b")
	step(tea.KeyDown, "y", "b")
	step(tea.KeyDown, "b", "b")
	step(tea.KeyBackspace, "b", "root")
}

func TestSearchRefreshesAfterZoomOut(t *testing.T) {
	m := testModel(t, map[string]int64{"root;a;work-a": 50, "root;b;work-b": 50})
	pressKey(&m, tea.KeyDown, "")
	pressKey(&m, tea.KeyEnter, "")
	pressKey(&m, tea.KeyRunes, "/")
	pressKey(&m, tea.KeyRunes, "work")
	pressKey(&m, tea.KeyCtrlA, "")
	pressKey(&m, tea.KeyEnter, "")
	require.Len(t, m.matches, 1)
	require.Equal(t, "work-a", m.selectedNode().Label)
	pressKey(&m, tea.KeyBackspace, "")
	require.Len(t, m.matches, 2)
	for _, name := range []string{"work-a", "work-b"} {
		pressKey(&m, tea.KeyRunes, "n")
		require.Equal(t, name, m.selectedNode().Label)
	}
}

func TestGraphGeometryAndTinyFrames(t *testing.T) {
	m := testModel(t, map[string]int64{
		"root;cold;leaf":  1,
		"root;left;child": 500,
		"root;left":       200,
		"root;right":      299,
	})
	m.width, m.height = 100, 20
	graph := viewRows(t, &m)
	require.Equal(t, 70, strings.Index(graph[headerLines+1], "right"))
	require.Equal(t, 20, strings.Index(graph[headerLines+2], "child"))
	for _, size := range [][2]int{{1, 1}, {12, 8}, {40, 20}} {
		m.width, m.height = size[0], size[1]
		viewRows(t, &m)
	}
	for depth, name := range []string{"cold", "leaf"} {
		pressKey(&m, tea.KeyDown, "")
		rows := viewRows(t, &m)
		require.True(t, strings.HasPrefix(rows[headerLines+1+depth], ">"), "%s cursor", name)
		require.Contains(t, strings.Join(rows, "\n"), "selected: "+name+"\n")
	}
	pressKey(&m, tea.KeyRunes, "v")
	view := strings.Join(viewRows(t, &m), "\n")
	require.Regexp(t, `(?m)^ +cold +0\.10%$`, view)
	require.Regexp(t, `(?m)^> +leaf +0\.10%$`, view)
	pressKey(&m, tea.KeyRunes, "/")
	pressKey(&m, tea.KeyRunes, "cold")
	pressKey(&m, tea.KeyEnter, "")
	pressKey(&m, tea.KeyEnter, "")
	require.Equal(t, "cold", m.visible.root().Label)
	pressKey(&m, tea.KeyBackspace, "")
	require.Equal(t, "root", m.visible.root().Label)
	require.Equal(t, viewFrames, m.view)
	pressKey(&m, tea.KeyRunes, "v")
	require.Equal(t, viewGraph, m.view)
	require.Equal(t, "cold", m.selectedNode().Label)
}

func TestTerminalTextAndRawSearch(t *testing.T) {
	const root = "根\x1b]0;title\a"
	const raw = "函数\x1b[2J\n\r\u009b\u202e\xff"
	const escaped = `函数\x1b[2J\n\r\u009b\u202e\xff`
	tree := NewStackTree(t.Context())
	require.NoError(t, tree.Add([]string{root, raw}, 1))
	m := buildTestModel(t, tree)
	m.query = "\x1b[2J"
	m.applySearch()
	require.Equal(t, raw, m.selectedNode().Label)
	require.Equal(t, m.query, m.lastSearch)
	m.updateSearch(tea.KeyMsg{Type: tea.KeyBackspace})
	require.Equal(t, "\x1b[2", m.query)
	m.width, m.height = 120, 20
	for _, mode := range []viewMode{viewGraph, viewFrames} {
		m.view = mode
		rawView := m.View()
		require.NotRegexp(t, "\x1b(?:\\[2J|]0;title)|[\r\a\u009b\u202e]", rawView)
		require.True(t, utf8.ValidString(rawView), "mode %d rendered invalid UTF-8", mode)
		view := strings.Join(viewRows(t, &m), "\n")
		require.Contains(t, view, `focus=根\x1b]0;title\a`)
		require.Contains(t, view, `search: \x1b[2J`)
		require.Contains(t, view, "selected: "+escaped)
		require.Contains(t, view, `path: 根\x1b]0;title\a > `+escaped)
	}
	require.Equal(t, " >xy", ansi.Strip(overlayCursor("\x1b[45m界xy\x1b[0m", 1, ">")))
	require.Equal(t, "函数...", truncate("函数调用路径", 5))
}

func TestFrameColumn(t *testing.T) {
	require.Equal(t, 100, frameColumn(math.MaxInt64, math.MaxInt64, 100), "full")
	require.Equal(t, 99, frameColumn(math.MaxInt64-1, math.MaxInt64, 100), "just below full")
	require.Equal(t, 50, frameColumn(1<<59, 1<<60, 100), "large half")
	require.Equal(t, 100, frameColumn(math.MaxInt64, 1, 100), "above total")
	require.Zero(t, frameColumn(1, 1000, 100), "less than one column")
	require.Zero(t, frameColumn(0, 100, 100), "zero weight")
	require.Zero(t, frameColumn(100, 0, 100), "zero total")
	require.Zero(t, frameColumn(-1, 100, 100), "negative weight")
}

func TestGroupingReorderReusesFunctionNodes(t *testing.T) {
	m := testModel(t, map[string]int64{"cpu0;comm=api;request": 2, "cpu0;comm=worker;work": 3})
	originalRoot := m.tree.root()
	originalParent := originalRoot.Children[0]
	function := originalParent.Children[0]
	m.focusOn(originalParent)

	m.grouping = &groupingState{
		ctx:   cancelOnCheck(t, 4),
		tags:  []string{"cpu", "comm"},
		input: "comm,cpu",
		buildTree: func(context.Context, []string) (*StackTree, error) {
			t.Fatal("same-label reorder called the full tree builder")
			return nil, nil
		},
	}
	reorder := func() groupingResult {
		t.Helper()
		cmd := m.submitGrouping()
		require.NotNil(t, cmd, "reorder did not start")
		return cmd().(groupingResult)
	}
	result := reorder()
	m.applyGrouping(&result)
	require.ErrorIs(t, result.err, context.Canceled)
	require.Same(t, originalRoot, m.tree.root())
	require.Same(t, originalParent, m.visible.root())
	require.Same(t, originalParent, function.Parent)
	require.Equal(t, []string{"cpu", "comm"}, m.grouping.tags)

	m.grouping.ctx = t.Context()
	result = reorder()
	require.NoError(t, result.err)
	require.Same(t, originalParent, function.Parent, "only the event loop may reparent nodes")
	m.applyGrouping(&result)
	require.Len(t, m.tree, 7)
	require.Same(t, function, m.tree[3])
	require.Equal(t, "total > comm=api > cpu0 > request", nodePath(function))
	require.Equal(t, [2]int64{2, 2}, [2]int64{function.Value, function.Self})
	require.EqualValues(t, 5, m.visible.root().Value)

	m.grouping.input = "cpu,comm"
	result = reorder()
	m.applyGrouping(&result)
	require.NoError(t, result.err)
	require.Equal(t, "cpu0 > comm=api > request", nodePath(function))
	require.EqualValues(t, 5, m.tree.root().Value)
}

func testModel(t *testing.T, paths map[string]int64) Model {
	t.Helper()
	tree := NewStackTree(t.Context())
	for path, weight := range paths {
		require.NoError(t, tree.Add(strings.Split(path, ";"), weight), "stack %q", path)
	}

	return buildTestModel(t, tree)
}

func buildTestModel(t *testing.T, tree *StackTree) Model {
	t.Helper()
	model, err := tree.BuildModel()
	require.NoError(t, err)
	require.NotNil(t, model)

	return *model
}

func pressKey(m *Model, kind tea.KeyType, text string) {
	updated, _ := m.Update(tea.KeyMsg{
		Type:  kind,
		Runes: []rune(text),
	})
	*m = updated.(Model)
}

func cancelOnCheck(t *testing.T, checks int) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	return &cancelDuringWork{
		Context: ctx,
		cancel:  cancel,
		checks:  checks,
	}
}

func viewRows(t *testing.T, m *Model) []string {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(ansi.Strip(m.View()), "\n"), "\n")
	require.LessOrEqual(t, len(lines), m.height, "view height")
	for _, line := range lines {
		require.LessOrEqual(t, ansi.StringWidth(line), m.width, "row %q", line)
	}

	return lines
}

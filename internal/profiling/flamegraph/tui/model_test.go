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
	"math"
	"strings"
	"testing"

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

func TestModelBarWidthLargeValues(t *testing.T) {
	model := NewModel(nil)
	model.width = 120
	tests := []struct {
		name  string
		value int64
		total int64
		want  int
	}{
		{name: "full maximum", value: math.MaxInt64, total: math.MaxInt64, want: 40},
		{name: "just below maximum", value: math.MaxInt64 - 1, total: math.MaxInt64, want: 39},
		{name: "large half", value: 1 << 59, total: 1 << 60, want: 20},
		{name: "large three quarters", value: 3 << 58, total: 1 << 60, want: 30},
		{name: "above total", value: math.MaxInt64, total: 1, want: 40},
		{name: "ordinary quarter", value: 25, total: 100, want: 10},
		{name: "small sample", value: 1, total: 100, want: barMinWidth},
		{name: "zero sample", total: 100, want: barMinWidth},
		{name: "zero total", value: 100, want: barMinWidth},
		{name: "negative sample", value: -1, total: 100, want: barMinWidth},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := model.barWidth(tt.value, tt.total); got != tt.want {
				t.Fatalf("barWidth(%d, %d) = %d, want %d", tt.value, tt.total, got, tt.want)
			}
		})
	}
}

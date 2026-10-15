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
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ccfos/huatuo/internal/flamegraph"
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

func TestModelSearchTracksFocusedFrames(t *testing.T) {
	model := NewModel([]flamegraph.FrameData{
		{Level: 0, Value: 100, Label: "root"},
		{Level: 1, Value: 50, Label: "request-a"},
		{Level: 2, Value: 50, Label: "handle"},
		{Level: 1, Value: 50, Label: "request-b"},
		{Level: 2, Value: 50, Label: "handle"},
	})
	key := func(k tea.KeyMsg) {
		updated, _ := model.Update(k)
		model = updated.(Model)
	}
	key(tea.KeyMsg{Type: tea.KeyDown})
	key(tea.KeyMsg{Type: tea.KeyEnter})
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("handle")})
	key(tea.KeyMsg{Type: tea.KeyEnter})
	if len(model.matches) != 1 {
		t.Fatalf("branch matches = %d, want 1", len(model.matches))
	}
	key(tea.KeyMsg{Type: tea.KeyLeft})
	if model.selectedNode().Label != "request-a" {
		t.Fatalf("zoom out selection = %q", model.selectedNode().Label)
	}
	if !strings.Contains(model.View(), "matches=2") {
		t.Fatalf("newly visible match missing: %s", model.View())
	}
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	first := model.selectedNode()
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	second := model.selectedNode()
	if first == second || first.Label != "handle" || second.Label != "handle" {
		t.Fatal("next match does not visit both handles")
	}
	key(tea.KeyMsg{Type: tea.KeyUp})
	key(tea.KeyMsg{Type: tea.KeyEnter})
	if model.focus.Label != "request-b" || len(model.matches) != 1 || !model.matches[second] {
		t.Fatalf("zoom in retains matches outside focus: %s", model.View())
	}
	if model.selectedNode() != model.focus {
		t.Fatal("zoom in should keep the focus selected")
	}
	key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	key(tea.KeyMsg{Type: tea.KeyEnter})
	key(tea.KeyMsg{Type: tea.KeyLeft})
	if len(model.matches) != 0 || model.lastSearch != "" {
		t.Fatal("cleared query restored after zoom")
	}
}

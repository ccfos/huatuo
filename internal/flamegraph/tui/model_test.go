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
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

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
	if got != "函..." {
		t.Fatalf("truncate() = %q, want 函...", got)
	}
}

func TestModelRowsFitTerminal(t *testing.T) {
	for _, width := range []int{12, 24, 40, 80, 120} {
		for _, label := range []string{"frame", "函数调用路径"} {
			t.Run(fmt.Sprintf("%d/%s", width, label), func(t *testing.T) {
				frames := make([]flamegraph.FrameData, 20)
				for depth := range frames {
					frames[depth] = flamegraph.FrameData{Level: int64(depth), Value: 100, Label: label}
				}
				model := NewModel(frames)
				resized, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: 40})
				model = resized.(Model)
				model.query = label
				model.applySearch()
				selected, _ := model.Update(tea.KeyMsg{Type: tea.KeyEnd})
				model = selected.(Model)
				rows := strings.Split(model.View(), "\n")[headerLines : headerLines+len(frames)]
				for depth, row := range rows {
					if ansi.StringWidth(row) > width {
						t.Errorf("depth %d: row width %d exceeds %d: %q", depth, ansi.StringWidth(row), width, row)
					}
					rendered := ansi.Truncate(row, width, "")
					if !strings.HasSuffix(rendered, "100.00%") {
						t.Errorf("depth %d: percentage clipped: %q", depth, rendered)
					}
					if depth == len(frames)-1 && !strings.HasPrefix(rendered, ">*") {
						t.Errorf("selected search match markers missing: %q", rendered)
					}
					if width >= 80 && !strings.Contains(rendered, label) {
						t.Errorf("depth %d: label lost with enough room: %q", depth, rendered)
					}
				}
			})
		}
	}
}

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
)

func TestModelViewFitsTerminalHeight(t *testing.T) {
	for height := 1; height <= 20; height++ {
		t.Run(fmt.Sprint(height), func(t *testing.T) {
			model := NewModel(sampleFrames())
			updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: height})
			model = updated.(Model)
			view := model.View()
			if lines := len(strings.Split(view, "\n")); lines > height {
				t.Fatalf("View has %d lines in a %d-line terminal:\n%s", lines, height, view)
			}
			if !strings.Contains(view, "HUATUO") {
				t.Fatal("title is missing")
			}
			if !strings.Contains(view, "q quit") {
				t.Fatal("quit hint is missing")
			}
			if height >= 9 && !strings.Contains(view, "selected:") {
				t.Fatal("selection details are missing")
			}
		})
	}
}

func TestModelResizePreservesSelection(t *testing.T) {
	model := NewModel(sampleFrames())
	model, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyEnd})
	selected := model.selectedNode()
	for _, height := range []int{10, 9, 5, 1, 20} {
		updated, _ := model.Update(tea.WindowSizeMsg{Width: 120, Height: height})
		model = updated.(Model)
		if model.selectedNode() != selected {
			t.Fatal("resize changed selected frame")
		}
		if model.offset < 0 || model.offset > model.cursor {
			t.Fatalf("offset=%d cursor=%d", model.offset, model.cursor)
		}
		if height >= 9 && !strings.Contains(model.View(), "> ") {
			t.Fatal("selected row is not visible")
		}
	}
	model, _ = model.updateKey(tea.KeyMsg{Type: tea.KeyUp})
	if model.selectedNode() == selected {
		t.Fatal("navigation stopped working after resize")
	}
}

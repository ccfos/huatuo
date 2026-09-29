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

	"github.com/ccfos/huatuo/internal/flamegraph"
)

func TestControlLabelKeepsTUIGeometry(t *testing.T) {
	baseline := NewModel([]flamegraph.FrameData{{Level: 0, Value: 1, Label: "audit-forged"}})
	withControl := NewModel([]flamegraph.FrameData{{Level: 0, Value: 1, Label: "audit\nforged"}})
	if got, want := strings.Count(withControl.View(), "\n"), strings.Count(baseline.View(), "\n"); got != want {
		t.Fatalf("one label added %d extra terminal rows", got-want)
	}
}

func TestControlSearchKeepsTUIGeometry(t *testing.T) {
	baseline := NewModel(sampleFrames())
	baseline.searching = true
	baseline.query = "audit-forged"
	withControl := NewModel(sampleFrames())
	withControl.searching = true
	withControl.query = "audit\nforged"
	if got, want := strings.Count(withControl.View(), "\n"), strings.Count(baseline.View(), "\n"); got != want {
		t.Fatalf("one search query added %d extra terminal rows", got-want)
	}
}

func TestEscapedLabelsKeepOriginalSearchIdentity(t *testing.T) {
	const label = "worker\nline"
	model := NewModel([]flamegraph.FrameData{{Level: 0, Value: 1, Label: label}})
	model.query = label
	model.applySearch()
	if model.focus.Label != label || model.lastSearch != label || !model.matches[model.focus] {
		t.Fatal("display escaping changed search identity")
	}
	view := model.View()
	if strings.Contains(view, label) || !strings.Contains(view, `worker\nline`) {
		t.Fatalf("unescaped or missing display field: %q", view)
	}
}

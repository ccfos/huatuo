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

package flamegraph

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"
	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/types"
)

func TestRenderKeepsSymbolsOutOfJavaScript(t *testing.T) {
	symbol := `');alert(1);//<script>alert(2)</script>`
	var proc processor
	proc.Process(Stack{
		Names:   []string{"root", symbol},
		Samples: 1,
	})

	var output bytes.Buffer
	if err := proc.render(&output, DefaultStyle); err != nil {
		t.Fatalf("render flame graph: %v", err)
	}

	content := output.String()
	if strings.Contains(content, `onmouseover="s('`) ||
		strings.Contains(content, "<script>alert(2)</script>") {
		t.Fatalf("SVG embeds an untrusted symbol as executable markup: %s", content)
	}

	if !strings.Contains(content, `data-title="`) ||
		!strings.Contains(content, "&lt;script&gt;alert(2)&lt;/script&gt;") {
		t.Fatalf("SVG does not preserve the escaped symbol as data: %s", content)
	}
}

func TestTUIRejectsFileDestination(t *testing.T) {
	_, err := NewTUIFormatter(output.Config{Destination: t.TempDir()})
	const want = "tui output requires --output-path to be omitted or set to stdout or -"
	if err == nil || err.Error() != want {
		t.Fatalf("destination validation=%v, want %q", err, want)
	}
}

func TestTUIKeepsSignedTotalsAcrossWindows(t *testing.T) {
	f := &tuiFormatter{aggregator: aggregation.New()}
	w := &types.AggregationWindow{Aggregated: types.AggregatedProfile{
		Frames: []types.ProfileFrame{{Name: "root"}, {Name: "leaf"}},
		Stacks: [][]uint32{{0, 1}},
		Samples: []types.AggregatedSample{{
			UserStack: 1,
			Value:     10,
		}},
	}}
	if err := f.Add(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	w.Aggregated.Samples[0].Value = -10
	if err := f.Add(t.Context(), w); err != nil {
		t.Fatal(err)
	}
	if err := f.Complete(t.Context()); err != nil {
		t.Fatalf("balanced profile must not open a viewer: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if !f.aggregator.Empty() {
		t.Fatal("close retained profile")
	}
}

func TestTUICancellationDoesNotPublish(t *testing.T) {
	f := &tuiFormatter{aggregator: aggregation.New()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.Add(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("Add cancellation=%v", err)
	}
	if err := f.Complete(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Complete cancellation=%v", err)
	}
	if !f.aggregator.Empty() {
		t.Fatal("cancellation changed the profile")
	}
}

func TestTUICompletePreparesWithoutOpeningTerminal(t *testing.T) {
	f := &tuiFormatter{aggregator: aggregation.New()}
	if err := f.aggregator.Add(&aggregation.Sample{
		UserFrames: []types.ProfileFrame{{Name: "main"}},
		Value:      1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := f.Complete(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.model == nil || !strings.Contains(f.model.View(), "main") {
		t.Fatal("completion did not prepare the sampled frame")
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := f.View(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled viewing must not open a terminal: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if f.model != nil || !f.aggregator.Empty() {
		t.Fatal("close retained the prepared model or samples")
	}
}

func TestTUIPreservesFrameNamesThroughCompletion(t *testing.T) {
	f := &tuiFormatter{aggregator: aggregation.New()}
	t.Cleanup(func() { _ = f.Close() })
	for _, name := range []string{"foo;bar", "foo:bar", "foo\nbar", "foo bar"} {
		if err := f.aggregator.Add(&aggregation.Sample{
			UserFrames: []types.ProfileFrame{{Name: "main"}, {Name: name}},
			Value:      10,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Complete(t.Context()); err != nil {
		t.Fatal(err)
	}
	if f.model == nil || !strings.Contains(f.model.View(), "children: 4") {
		t.Fatal("completion merged distinct source frames")
	}

	var model tea.Model = f.model
	for _, name := range []string{`foo\nbar`, "foo bar", "foo:bar", "foo;bar"} {
		model, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
		view := model.View()
		if !strings.Contains(view, "selected: "+name) || !strings.Contains(view, "value: 10  self: 10  focus: 25.00%") {
			t.Fatalf("frame %q lost its name or independent weight: %q", name, view)
		}
	}
}

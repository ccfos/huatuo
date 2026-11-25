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
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"
	"github.com/ccfos/huatuo/pkg/profiling"
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
	for i, name := range []string{`foo\nbar`, "foo bar", "foo:bar", "foo;bar"} {
		key := tea.KeyRight
		if i == 0 {
			key = tea.KeyDown
		}
		model, _ = model.Update(tea.KeyMsg{Type: key})
		view := model.View()
		if !strings.Contains(view, "selected: "+name) || !strings.Contains(view, "value: 10  self: 10  focus: 25.00%") {
			t.Fatalf("frame %q lost its name or independent weight: %q", name, view)
		}
	}
}

func TestTUIReportReplayGroupsLabelsAndSortsTopTable(t *testing.T) {
	t.Parallel()

	report := tuiReport{
		Version:     tuiReportVersion,
		ProfileType: profiling.ProfileTypeCpuSample,
		Aggregated: types.AggregatedProfile{
			Frames: []types.ProfileFrame{{Name: "main"}, {Name: "work"}, {Name: "io"}},
			Stacks: [][]uint32{{0, 1}, {0, 2}},
			LabelSets: [][]types.ProfileLabel{
				{types.ProfileStringLabel("comm", "api"), types.ProfileCPULabel(0), types.ProfilePIDLabel(10)},
				{types.ProfileStringLabel("comm", "api"), types.ProfileCPULabel(7), types.ProfilePIDLabel(11)},
			},
			Samples: []types.AggregatedSample{
				{UserStack: 1, LabelSet: 1, Value: 2},
				{UserStack: 1, LabelSet: 2, Value: 3},
				{UserStack: 2, Value: 1},
			},
		},
	}
	path := filepath.Join(t.TempDir(), "profile.gob")
	var data bytes.Buffer
	require.NoError(t, writeTUIReport(&data, &report))
	require.NoError(t, os.WriteFile(path, data.Bytes(), 0o600))

	saved, err := readTUIReport(path)
	require.NoError(t, err)
	model, err := prepareViewer(t.Context(), &saved.Aggregated, "nanoseconds")
	require.NoError(t, err)
	require.NotNil(t, model)
	require.Contains(t, model.View(), "focus=main")
	require.Contains(t, model.View(), "groups: none")
	require.Contains(t, model.View(), "value: 6")
	require.NotContains(t, model.View(), "cpu0")

	var viewer tea.Model = model
	key := func(keyType tea.KeyType) tea.Cmd {
		var cmd tea.Cmd
		viewer, cmd = viewer.Update(tea.KeyMsg{Type: keyType})
		return cmd
	}
	typeText := func(value string) {
		viewer, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(value)})
	}
	submit := func(value string) tea.Cmd {
		typeText("g")
		key(tea.KeyCtrlU)
		typeText(value)
		return key(tea.KeyEnter)
	}
	complete := func(cmd tea.Cmd) {
		t.Helper()
		require.NotNil(t, cmd)
		viewer, _ = viewer.Update(cmd())
	}

	cmd := submit("cpu")
	require.Contains(t, viewer.View(), "grouping stacks")
	require.Contains(t, viewer.View(), "focus=main")
	typeText("g")
	require.Nil(t, key(tea.KeyEnter), "an in-flight grouping cannot start a second build")
	complete(cmd)
	require.Contains(t, viewer.View(), "groups: cpu (g edit)")
	require.Contains(t, viewer.View(), "value: 6")
	for _, group := range []string{"cpu0", "cpu7", "cpu=?"} {
		require.Contains(t, viewer.View(), group)
	}
	key(tea.KeyDown)
	require.Nil(t, submit("  cpu  "), "submitting the current grouping does not rebuild")
	require.Contains(t, viewer.View(), "selected: cpu0")
	require.Nil(t, submit("q"))
	require.Contains(t, viewer.View(), `invalid grouping label "q"`)
	key(tea.KeyEsc)
	require.Nil(t, submit("cpu,cpu"))
	require.Contains(t, viewer.View(), `duplicate grouping label "cpu"`)
	key(tea.KeyEsc)

	complete(submit("cpu, comm"))
	key(tea.KeyDown)
	key(tea.KeyDown)
	require.Contains(t, viewer.View(), `path: total > cpu0 > comm="api"`)
	complete(submit("comm,cpu,pid"))
	key(tea.KeyDown)
	key(tea.KeyDown)
	key(tea.KeyDown)
	require.Contains(t, viewer.View(), `path: total > comm="api" > cpu0 > pid="10"`)
	require.Contains(t, viewer.View(), "value: 2")

	viewKey := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}}
	viewer, _ = viewer.Update(viewKey)
	viewer, _ = viewer.Update(viewKey)
	selectedRow := func(view string) []string {
		t.Helper()
		_, selected, ok := strings.Cut(view, "\n> ")
		require.True(t, ok, "table has no selected row")
		row, _, _ := strings.Cut(selected, "\n")
		return strings.Fields(row)
	}
	require.Contains(t, viewer.View(), "sort: Self")
	require.Equal(t, []string{"work", "5", "5"}, selectedRow(viewer.View()))

	viewer, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	require.Contains(t, viewer.View(), "sort: Total")
	require.Equal(t, []string{"main", "0", "6"}, selectedRow(viewer.View()))

	typeText("v")
	require.Nil(t, submit(""), "clearing the grouping reuses the default tree")
	require.Contains(t, viewer.View(), "focus=main")
	require.Contains(t, viewer.View(), "groups: none")
	require.Contains(t, viewer.View(), "value: 6")

	cmd = submit("cpu")
	typeText("q")
	complete(cmd)
	require.Contains(t, viewer.View(), "focus=main", "a grouping result cannot replace the view after q")
}

func TestReadTUIReportRejectsDirectory(t *testing.T) {
	t.Parallel()

	_, err := readTUIReport(t.TempDir())
	require.ErrorContains(t, err, "report must be a regular file")
}

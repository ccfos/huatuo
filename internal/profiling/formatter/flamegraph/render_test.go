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

func TestTUICombinesSignedWindows(t *testing.T) {
	f := &tuiFormatter{aggregator: aggregation.New()}
	w := &types.AggregationWindow{Aggregated: types.AggregatedProfile{
		Frames:  []types.ProfileFrame{{Name: "main"}},
		Stacks:  [][]uint32{{0}},
		Samples: []types.AggregatedSample{{UserStack: 1}},
	}}
	w.Aggregated.Samples[0].Value = 10
	require.NoError(t, f.Add(t.Context(), w))
	require.False(t, f.aggregator.Empty(), "positive window must be collected")
	w.Aggregated.Samples[0].Value = -10
	require.NoError(t, f.Add(t.Context(), w))
	require.NoError(t, f.Complete(t.Context()))
	require.Nil(t, f.model, "balanced windows must produce no viewer")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, f.Add(ctx, nil), context.Canceled)
	require.ErrorIs(t, f.Complete(ctx), context.Canceled)
	require.Nil(t, f.aggregator)
}

func TestTUICompletionPreservesFrames(t *testing.T) {
	f := &tuiFormatter{aggregator: aggregation.New()}
	for _, name := range []string{"foo;bar", "foo:bar", "foo\nbar", "foo\rbar", "foo bar"} {
		require.NoError(t, f.aggregator.Add(&aggregation.Sample{
			UserFrames: []types.ProfileFrame{{Name: "main"}, {Name: name}},
			Value:      10,
		}))
	}
	require.NoError(t, f.Complete(t.Context()))
	require.NotNil(t, f.model)
	var model tea.Model = f.model
	require.Contains(t, model.View(), "children: 5")
	model, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	for _, name := range []string{`foo\nbar`, `foo\rbar`, "foo bar", "foo:bar", "foo;bar"} {
		view := model.View()
		require.Contains(t, view, "selected: "+name+"\n")
		require.Contains(t, view, "value: 10  self: 10  focus: 20.00%")
		model, _ = model.Update(tea.KeyMsg{Type: tea.KeyRight})
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, f.View(ctx), context.Canceled)
	require.NoError(t, f.Close())
	require.Nil(t, f.model)
	require.Nil(t, f.aggregator)
}

func TestTUIReportReplay(t *testing.T) {
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
	dir := t.TempDir()
	path := filepath.Join(dir, "profile.gob")
	var data bytes.Buffer
	require.NoError(t, writeTUIReport(&data, &report))
	require.NoError(t, os.WriteFile(path, data.Bytes(), 0o600))
	saved, err := readTUIReport(path)
	require.NoError(t, err)
	require.Equal(t, report, saved)
	_, err = readTUIReport(dir)
	require.ErrorContains(t, err, "report must be a regular file")

	model, err := prepareViewer(t.Context(), &saved.Aggregated, "nanoseconds")
	require.NoError(t, err)
	require.NotNil(t, model)
	var viewer tea.Model = model
	update := func(key tea.KeyType, runes ...rune) tea.Cmd {
		var cmd tea.Cmd
		viewer, cmd = viewer.Update(tea.KeyMsg{
			Type:  key,
			Runes: runes,
		})

		return cmd
	}
	submit := func(tags string) tea.Cmd {
		update(tea.KeyRunes, 'g')
		update(tea.KeyCtrlU)
		update(tea.KeyRunes, []rune(tags)...)
		return update(tea.KeyEnter)
	}
	adopt := func(cmd tea.Cmd) {
		t.Helper()
		require.NotNil(t, cmd)
		viewer, _ = viewer.Update(cmd())
	}
	show := func(parts ...string) {
		t.Helper()
		view := viewer.View()
		for _, part := range parts {
			require.Contains(t, view, part)
		}
	}

	show("groups: none", "focus=main", "value: 6")
	require.NotContains(t, viewer.View(), "cpu0")
	cmd := submit("comm,cpu,pid")
	show("grouping stacks", "focus=main")
	require.Nil(t, submit("cpu"), "a pending grouping cannot start another build")
	adopt(cmd)
	show("groups: comm,cpu,pid", "cpu0", "cpu7", "cpu=?", "value: 6")
	for range 3 {
		update(tea.KeyDown)
	}
	require.Nil(t, submit(" comm, cpu, pid "), "same grouping must retain the view")
	show(`path: total > comm="api" > cpu0 > pid="10"`, "value: 2")
	for input, want := range map[string]string{
		"q":       `invalid grouping label "q"`,
		"cpu,cpu": `duplicate grouping label "cpu"`,
	} {
		require.Nil(t, submit(input))
		show(want)
		update(tea.KeyEsc)
	}
	update(tea.KeyRunes, 'v')
	update(tea.KeyRunes, 'v')
	show("sort: Self")
	require.Regexp(t, `(?m)^> work[ \t]+5[ \t]+5[ \t]*$`, viewer.View())
	update(tea.KeyRunes, 's')
	show("sort: Total")
	require.Regexp(t, `(?m)^> main[ \t]+0[ \t]+6[ \t]*$`, viewer.View())

	update(tea.KeyRunes, 'v')
	require.Nil(t, submit(""), "clearing grouping reuses the default tree")
	show("groups: none", "focus=main", "value: 6")
	cmd = submit("cpu")
	update(tea.KeyRunes, 'q')
	adopt(cmd)
	show("focus=main")
}

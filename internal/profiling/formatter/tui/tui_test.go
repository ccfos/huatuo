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
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"

	"github.com/ccfos/huatuo/internal/profiling/aggregation"
	output "github.com/ccfos/huatuo/internal/profiling/formatter"
	"github.com/ccfos/huatuo/pkg/profiling"
	"github.com/ccfos/huatuo/pkg/types"
)

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

func TestTUIWithOnlyDroppedSamplesSucceeds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.gob")
	destination, err := output.NewLocalDestination(output.Config{Destination: path, Format: output.FormatTUI})
	require.NoError(t, err)
	f := &tuiFormatter{aggregator: aggregation.New(), destination: destination}
	require.NoError(t, f.Add(t.Context(), &types.AggregationWindow{
		ProfileType:    profiling.ProfileTypeIRQTracingSample,
		DroppedSamples: map[string]uint64{types.DropReasonCollection: 1},
	}))
	require.NoError(t, f.Complete(t.Context()))
	require.NoError(t, f.View(t.Context()))
	require.NoFileExists(t, path)
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

func TestTUIGobReplay(t *testing.T) {
	profile := tuiProfile{
		Version:     tuiGobVersion,
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
	require.NoError(t, writeTUIGob(t.Context(), &data, &profile))
	require.NoError(t, os.WriteFile(path, data.Bytes(), 0o600))
	saved, err := readTUIGob(t.Context(), path)
	require.NoError(t, err)
	require.Equal(t, profile, saved)
	_, err = readTUIGob(t.Context(), dir)
	require.ErrorContains(t, err, "report must be a regular file")

	model, err := buildViewerModel(t.Context(), &saved, "nanoseconds")
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
	submit := func(groupLabelNames string) tea.Cmd {
		update(tea.KeyRunes, 'g')
		update(tea.KeyCtrlU)
		update(tea.KeyRunes, []rune(groupLabelNames)...)
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

	update(tea.KeyEnter)
	show("view: all frames", "selected: main")
	require.Nil(t, submit(""), "clearing grouping reuses the default tree")
	show("groups: none", "focus=main", "value: 6")
	cmd = submit("cpu")
	update(tea.KeyRunes, 'q')
	adopt(cmd)
	show("focus=main")
}

func TestTUIGobRejectsUnknownDropReason(t *testing.T) {
	var encoded bytes.Buffer
	require.NoError(t, writeTUIGob(t.Context(), &encoded, &tuiProfile{
		Version:        tuiGobVersion,
		ProfileType:    profiling.ProfileTypeIRQTracingSample,
		DroppedSamples: map[string]uint64{"other_loss": 7},
	}))
	_, err := decodeTUIGob(t.Context(), &encoded)
	require.ErrorContains(t, err, `unsupported profiling drop reason "other_loss"`)
}

func TestTUIGobRetainsIRQLabelsAndDroppedSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "irq.gob")
	destination, err := output.NewLocalDestination(output.Config{Destination: path, Format: output.FormatTUI})
	require.NoError(t, err)
	f := &tuiFormatter{aggregator: aggregation.New(), destination: destination}
	for i, role := range []string{"source", "victim"} {
		w := &types.AggregationWindow{
			ProfileType:    profiling.ProfileTypeIRQTracingSample,
			DroppedSamples: map[string]uint64{types.DropReasonCollection: uint64(i + 1), types.DropReasonAggregationQueue: 1},
			Aggregated: types.AggregatedProfile{
				Frames:    []types.ProfileFrame{{Name: "work"}},
				Stacks:    [][]uint32{{0}},
				LabelSets: [][]types.ProfileLabel{{types.ProfileStringLabel("irq_context", role), types.ProfileStringLabel("irq_type", "softirq"), types.ProfileStringLabel("irq_vector", []string{"2", "3"}[i])}},
				Samples:   []types.AggregatedSample{{UserStack: 1, LabelSet: 1, Value: int64(i + 1)}},
			},
		}
		require.NoError(t, f.Add(t.Context(), w))
	}
	require.NoError(t, f.Complete(t.Context()))
	saved, err := readTUIGob(t.Context(), path)
	require.NoError(t, err)
	require.Equal(t, map[string]uint64{types.DropReasonCollection: 3, types.DropReasonAggregationQueue: 2}, saved.DroppedSamples)
	require.Contains(t, f.model.View(), "incomplete: collection=3, aggregation_queue=2")
	model, err := buildViewerModel(t.Context(), &saved, "samples")
	require.NoError(t, err)
	var viewer tea.Model = model
	viewer, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	viewer, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("irq_context,irq_vector")})
	viewer, cmd := viewer.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, cmd)
	viewer, _ = viewer.Update(cmd())
	require.Contains(t, viewer.View(), "groups: irq_context,irq_vector")
	for _, expected := range []string{"source", "victim", "NET_TX", "NET_RX", "value: 3", "incomplete: collection=3"} {
		require.Contains(t, viewer.View(), expected)
	}
	viewer, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyDown})
	require.Contains(t, viewer.View(), "selected: irq_context=\"source\"")
	require.Contains(t, viewer.View(), "value: 1 ")
	viewer, _ = viewer.Update(tea.KeyMsg{Type: tea.KeyRight})
	require.Contains(t, viewer.View(), "selected: irq_context=\"victim\"")
	require.Contains(t, viewer.View(), "value: 2 ")

	saved.Version = 1
	saved.DroppedSamples = nil
	var legacy bytes.Buffer
	require.NoError(t, writeTUIGob(t.Context(), &legacy, &saved))
	require.NoError(t, os.WriteFile(path, legacy.Bytes(), 0o600))
	saved, err = readTUIGob(t.Context(), path)
	require.NoError(t, err)
	model, err = buildViewerModel(t.Context(), &saved, "samples")
	require.NoError(t, err)
	require.Contains(t, model.View(), "completeness unknown")
}

type cancelOnRead struct {
	reader io.Reader
	cancel context.CancelFunc
}

func (r cancelOnRead) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.cancel()
	return n, err
}

func TestDecodeTUIGobObservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := decodeTUIGob(ctx, cancelOnRead{reader: bytes.NewBufferString(tuiGobMagic), cancel: cancel})
	require.ErrorIs(t, err, context.Canceled)
	_, err = readTUIGob(ctx, filepath.Join(t.TempDir(), "missing.gob"))
	require.ErrorIs(t, err, context.Canceled)
}

func BenchmarkLargeTUIProfile(b *testing.B) {
	const stackCount, depth = 16_384, 32
	profile := tuiProfile{Version: tuiGobVersion, Aggregated: types.AggregatedProfile{
		Frames:  make([]types.ProfileFrame, depth-1, depth-1+stackCount),
		Stacks:  make([][]uint32, stackCount),
		Samples: make([]types.AggregatedSample, stackCount),
	}}
	for i := range profile.Aggregated.Frames {
		profile.Aggregated.Frames[i].Name = fmt.Sprintf("shared_%02d", i)
	}
	for i := range profile.Aggregated.Samples {
		stack := make([]uint32, depth)
		for j := range depth - 1 {
			stack[j] = uint32(j)
		}
		stack[depth-1] = uint32(len(profile.Aggregated.Frames))
		profile.Aggregated.Frames = append(profile.Aggregated.Frames, types.ProfileFrame{Name: fmt.Sprintf("leaf_%05d", i)})
		profile.Aggregated.Stacks[i] = stack
		profile.Aggregated.Samples[i] = types.AggregatedSample{
			UserStack: uint32(i + 1), LabelSet: uint32(i%64 + 1), Value: int64(i + 1),
		}
	}
	for i := range 64 {
		profile.Aggregated.LabelSets = append(profile.Aggregated.LabelSets, []types.ProfileLabel{
			types.ProfileCPULabel(uint32(i % 8)),
			types.ProfileStringLabel("comm", fmt.Sprintf("worker_%02d", i/8)),
		})
	}
	b.Run("first_view", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			model, err := buildViewerModel(b.Context(), &profile, "samples")
			if err != nil {
				b.Fatal(err)
			}
			_ = model.View()
		}
	})
	b.Run("top_table_sort", func(b *testing.B) {
		model, err := buildViewerModel(b.Context(), &profile, "samples")
		if err != nil {
			b.Fatal(err)
		}
		key := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")}
		view, _ := model.Update(key)
		view, _ = view.Update(key)
		key.Runes = []rune("s")
		b.ReportAllocs()
		for b.Loop() {
			view, _ = view.Update(key)
			_ = view.View()
		}
	})
	for _, args := range []struct{ name, first, second string }{
		{"group_rebuild", "cpu", "comm"},
		{"group_reorder", "cpu,comm", "comm,cpu"},
	} {
		b.Run(args.name, func(b *testing.B) {
			model, err := buildViewerModel(b.Context(), &profile, "samples")
			if err != nil {
				b.Fatal(err)
			}
			var view tea.Model = model
			group := func(labelNames string) {
				view, _ = view.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
				view, _ = view.Update(tea.KeyMsg{Type: tea.KeyCtrlU})
				view, _ = view.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(labelNames)})
				nextView, cmd := view.Update(tea.KeyMsg{Type: tea.KeyEnter})
				if cmd == nil {
					b.Fatal("grouping did not start")
				}
				view, _ = nextView.Update(cmd())
			}
			group(args.first)
			b.ReportAllocs()
			first, second := args.first, args.second
			for b.Loop() {
				group(second)
				_ = view.View()
				first, second = second, first
			}
		})
	}
}

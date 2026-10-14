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

package aggregator

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/profiler"
	profctx "github.com/ccfos/huatuo/internal/profiler/context"
	"github.com/ccfos/huatuo/internal/profiler/output"

	pprof "github.com/google/pprof/profile"
	ptree "github.com/grafana/pyroscope/pkg/og/storage/tree"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func pprofTestSnapshot() *profiler.ProfileData {
	return &profiler.ProfileData{Profile: ptree.Profile{
		StringTable:       []string{"", "cpu", "nanoseconds", "leaf;λ", "main", "pid", "123", "test profile"},
		SampleType:        []*ptree.ValueType{{Type: 1, Unit: 2}},
		PeriodType:        &ptree.ValueType{Type: 1, Unit: 2},
		Period:            10000000,
		TimeNanos:         1791331200000000000,
		DurationNanos:     2000000000,
		DefaultSampleType: 1,
		Comment:           []int64{7},
		Function: []*ptree.Function{
			{Id: 1, Name: 3},
			{Id: 2, Name: 4},
		},
		Location: []*ptree.Location{
			{Id: 1, Line: []*ptree.Line{{FunctionId: 1}}},
			{Id: 2, Line: []*ptree.Line{{FunctionId: 2}}},
		},
		Sample: []*ptree.Sample{{
			LocationId: []uint64{1, 2},
			Value:      []int64{7},
			Label:      []*ptree.Label{{Key: 5, Str: 6}},
		}},
	}}
}

func TestWritePprofPreservesStandardProfile(t *testing.T) {
	dir := t.TempDir()
	snapshot := pprofTestSnapshot()
	require.NoError(t, writePprof(dir, snapshot))
	files, err := filepath.Glob(filepath.Join(dir, "pprof_*.pprof.gz"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	encoded, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.True(t, len(encoded) >= 2 && encoded[0] == 0x1f && encoded[1] == 0x8b, "output must be gzip-compressed")
	profile, err := pprof.ParseData(encoded)
	require.NoError(t, err)
	require.NoError(t, profile.CheckValid())
	require.Equal(t, snapshot.Profile.TimeNanos, profile.TimeNanos)
	require.Equal(t, snapshot.Profile.DurationNanos, profile.DurationNanos)
	require.Equal(t, snapshot.Profile.Period, profile.Period)
	require.Equal(t, "cpu", profile.DefaultSampleType)
	require.Equal(t, "cpu", profile.SampleType[0].Type)
	require.Equal(t, "nanoseconds", profile.SampleType[0].Unit)
	require.Equal(t, []string{"test profile"}, profile.Comments)
	require.Len(t, profile.Sample, 1)
	require.Equal(t, []int64{7}, profile.Sample[0].Value)
	require.Equal(t, []string{"123"}, profile.Sample[0].Label["pid"])
	require.Equal(t, "leaf;λ", profile.Sample[0].Location[0].Line[0].Function.Name)
	require.Equal(t, "main", profile.Sample[0].Location[1].Line[0].Function.Name)
}

func TestWritePprofRejectsInvalidSnapshotBeforeCreatingFile(t *testing.T) {
	for _, data := range []any{nil, (*profiler.ProfileData)(nil), "invalid"} {
		dir := t.TempDir()
		require.ErrorContains(t, writePprof(dir, data), "invalid pprof snapshot")
		files, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Empty(t, files)
	}
}

func TestWritePprofGoToolCompatibility(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writePprof(dir, pprofTestSnapshot()))
	files, err := filepath.Glob(filepath.Join(dir, "pprof_*.pprof.gz"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "tool", "pprof", "-top", files[0])
	report, err := command.CombinedOutput()
	require.NoError(t, err, "%s", report)
	require.Contains(t, string(report), "Type: cpu")
	require.Contains(t, string(report), "leaf;λ")
}

func TestPipelinePprofExportsOnlyFinalSnapshot(t *testing.T) {
	for _, oneShot := range []bool{false, true} {
		aggr := NewMockAggregator(t)
		pctx := &profctx.ProfilerContext{
			Ctx:          t.Context(),
			OutputFormat: output.FormatPprof,
			OutputPath:   t.TempDir(),
			IsOneShotAgg: oneShot,
		}
		aggr.On("Snapshot", mock.MatchedBy(func(actual *profctx.ProfilerContext) bool {
			return actual.OutputFormat == output.FormatPprof && actual.Ctx.Err() == nil
		})).Return(pprofTestSnapshot(), nil).Once()
		aggr.On("Reset").Return().Once()
		pipeline := NewPipeline(pctx, aggr)
		require.NoError(t, pipeline.aggregateAndSnapshot(context.Background(), false))
		entries, err := os.ReadDir(pctx.OutputPath)
		require.NoError(t, err)
		require.Empty(t, entries)
		pipeline.Start()
		pipeline.Stop()
		entries, err = os.ReadDir(pctx.OutputPath)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.True(t, strings.HasSuffix(entries[0].Name(), ".pprof.gz"))
		aggr.AssertNotCalled(t, "OutputFormatter")
	}
}

func TestPipelinePprofWriteFailureRetainsSnapshot(t *testing.T) {
	dir := t.TempDir()
	blocked := filepath.Join(dir, "regular-file")
	require.NoError(t, os.WriteFile(blocked, []byte("keep"), 0o600))
	aggr := NewMockAggregator(t)
	pctx := &profctx.ProfilerContext{OutputFormat: output.FormatPprof, OutputPath: blocked}
	aggr.On("Snapshot", pctx).Return(pprofTestSnapshot(), nil).Once()
	pipeline := NewPipeline(pctx, aggr)
	require.ErrorContains(t, pipeline.aggregateAndSnapshot(t.Context(), true), "write pprof output")
	aggr.AssertNotCalled(t, "Reset")
	contents, err := os.ReadFile(blocked)
	require.NoError(t, err)
	require.Equal(t, "keep", string(contents))
}

func TestPipelinePprofEmptySnapshotCreatesNoArtifact(t *testing.T) {
	aggr := NewMockAggregator(t)
	pctx := &profctx.ProfilerContext{OutputFormat: output.FormatPprof, OutputPath: t.TempDir()}
	aggr.On("Snapshot", pctx).Return(nil, nil).Once()
	pipeline := NewPipeline(pctx, aggr)
	require.NoError(t, pipeline.aggregateAndSnapshot(t.Context(), true))
	entries, err := os.ReadDir(pctx.OutputPath)
	require.NoError(t, err)
	require.Empty(t, entries)
	aggr.AssertNotCalled(t, "Reset")
}

func TestPprofUsesSnapshotWithoutRemoteUpload(t *testing.T) {
	require.True(t, output.FormatPprof.UsesSnapshot())
	require.False(t, output.FormatPprof.IsUpload())
	require.True(t, output.FormatRemote.UsesSnapshot())
	require.True(t, output.FormatRemote.IsUpload())
	formatter, err := NewFormatterForOutput(&profctx.ProfilerContext{OutputFormat: output.FormatPprof})
	require.NoError(t, err)
	require.Nil(t, formatter)
}

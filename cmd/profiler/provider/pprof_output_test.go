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

package provider

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/profiler"
	pcontext "github.com/ccfos/huatuo/internal/profiler/context"
	"github.com/ccfos/huatuo/internal/profiler/output"
	"github.com/ccfos/huatuo/internal/profiler/registry"
	"github.com/ccfos/huatuo/pkg/profiling"

	pprof "github.com/google/pprof/profile"
	"github.com/stretchr/testify/require"
)

type pprofFixtureSampler struct {
	record any
}

func (*pprofFixtureSampler) Start(*pcontext.ProfilerContext) error { return nil }
func (*pprofFixtureSampler) Stop(*pcontext.ProfilerContext) error  { return nil }

func (p *pprofFixtureSampler) ReadDataLoop(_ context.Context, enqueue func(any)) error {
	enqueue(p.record)
	return nil
}

func TestLocalPprofExportsAfterSamplingCancellation(t *testing.T) {
	for _, implementation := range []profiling.Implementation{
		profiling.ImplementationNative, profiling.ImplementationJava, profiling.ImplementationPython,
	} {
		t.Run(string(implementation), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			pctx := &pcontext.ProfilerContext{
				Ctx: ctx, Cancel: cancel, Type: profiling.TypeCPU, Mode: profiling.ModeOnCPU,
				Freq: 100, PIDs: []int{os.Getpid()}, OutputFormat: output.FormatPprof,
				OutputPath: t.TempDir(), Duration: 1,
			}
			meta, err := registry.Get(implementation, profiling.TypeCPU)
			require.NoError(t, err)
			var record any = profiler.SampleOutput{PID: os.Getpid(), Output: "main;leaf 3\n"}
			if implementation == profiling.ImplementationNative {
				record = &stackSample{
					Process:    processKey{PID: uint32(os.Getpid()), Comm: "worker"},
					StackTrace: symbolizedStackTrace{UserFrames: []string{"main", "leaf"}},
					Value:      3,
				}
			}
			meta.Impl = &pprofFixtureSampler{record: record}
			require.NoError(t, registry.Profile(pctx, meta))
			require.ErrorIs(t, ctx.Err(), context.Canceled)
			files, err := filepath.Glob(filepath.Join(pctx.OutputPath, "pprof_*.pprof.gz"))
			require.NoError(t, err)
			require.Len(t, files, 1, "sampling shutdown must flush the accepted record")
			encoded, err := os.ReadFile(files[0])
			require.NoError(t, err)
			profile, err := pprof.ParseData(encoded)
			require.NoError(t, err)
			require.NoError(t, profile.CheckValid())
			require.Len(t, profile.Sample, 1)
			require.Equal(t, []int64{3 * int64(time.Second) / 100}, profile.Sample[0].Value)
		})
	}
}

func TestLocalPprofSnapshotsPreserveProviderUnits(t *testing.T) {
	for _, language := range []profiling.Language{profiling.LanguageC, profiling.LanguageJava, profiling.LanguagePython} {
		t.Run(string(language), func(t *testing.T) {
			pctx := &pcontext.ProfilerContext{
				Ctx:          t.Context(),
				Type:         profiling.TypeCPU,
				Language:     language,
				Mode:         profiling.ModeOnCPU,
				OutputFormat: output.FormatPprof,
				Freq:         100,
				PIDs:         []int{os.Getpid()},
			}
			var snapshot any
			switch language {
			case profiling.LanguageC:
				aggr, err := newNativeAggregator(pctx)
				require.NoError(t, err)
				require.Nil(t, aggr.OutputFormatter())
				aggr.Aggregate(&stackSample{
					Process:    processKey{PID: 123, Comm: "worker"},
					StackTrace: symbolizedStackTrace{UserFrames: []string{"main", "leaf"}},
					Value:      3,
				})
				snapshot, err = aggr.Snapshot(pctx)
				require.NoError(t, err)
			case profiling.LanguageJava:
				aggr, err := newJavaAggregator(pctx)
				require.NoError(t, err)
				require.Nil(t, aggr.OutputFormatter())
				aggr.Aggregate(profiler.SampleOutput{PID: os.Getpid(), Output: "main;leaf 3\n"})
				snapshot, err = aggr.Snapshot(pctx)
				require.NoError(t, err)
			case profiling.LanguagePython:
				aggr, err := newPythonCPUAggregator(pctx)
				require.NoError(t, err)
				require.Nil(t, aggr.OutputFormatter())
				aggr.Aggregate(profiler.SampleOutput{PID: os.Getpid(), Output: "main;leaf 3\n"})
				snapshot, err = aggr.Snapshot(pctx)
				require.NoError(t, err)
			}
			data, ok := snapshot.(*profiler.ProfileData)
			require.True(t, ok, "snapshot type: %T", snapshot)
			require.Equal(t, profiler.ProfileTypeCpuSample, data.ProfileType)
			require.Len(t, data.Profile.SampleType, 1)
			typ := data.Profile.SampleType[0]
			require.Equal(t, "cpu", data.Profile.StringTable[typ.Type])
			require.Equal(t, "nanoseconds", data.Profile.StringTable[typ.Unit])
			require.Len(t, data.Profile.Sample, 1)
			require.Equal(t, []int64{3 * int64(time.Second) / 100}, data.Profile.Sample[0].Value)
			require.Contains(t, data.Profile.StringTable, "main")
			require.Contains(t, data.Profile.StringTable, "leaf")
		})
	}
}

func TestLocalPprofMemorySnapshotPreservesBytes(t *testing.T) {
	pctx := &pcontext.ProfilerContext{
		Ctx:          t.Context(),
		Type:         profiling.TypeMemory,
		Mode:         profiling.ModeVirtualAlloc,
		OutputFormat: output.FormatPprof,
	}
	aggr, err := newNativeAggregator(pctx)
	require.NoError(t, err)
	aggr.Aggregate(&stackSample{
		Process:    processKey{PID: 123, Comm: "worker"},
		StackTrace: symbolizedStackTrace{UserFrames: []string{"main", "allocate"}},
		Value:      512,
	})
	snapshot, err := aggr.Snapshot(pctx)
	require.NoError(t, err)
	data, ok := snapshot.(*profiler.ProfileData)
	require.True(t, ok)
	require.Equal(t, profiler.ProfileTypeMemSample, data.ProfileType)
	typ := data.Profile.SampleType[0]
	require.Equal(t, "bytes", data.Profile.StringTable[typ.Unit])
	require.Equal(t, []int64{512}, data.Profile.Sample[0].Value)
}

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
	"testing"
	"time"

	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
	"huatuo-bamai/pkg/profiling"

	pprof "github.com/google/pprof/profile"
)

func TestNativeAggregatorCollectionWindow(t *testing.T) {
	window := nativeTestCollectionWindow()
	tests := []struct {
		name string
		typ  profiling.Type
		mode profiling.Mode
		want int64
	}{
		{name: "on-CPU", typ: profiling.TypeCPU, mode: profiling.ModeOnCPU, want: 70_000_000},
		{name: "off-CPU", typ: profiling.TypeCPU, mode: profiling.ModeOffCPU, want: 7},
		{name: "virtual allocation", typ: profiling.TypeMemory, mode: profiling.ModeVirtualAlloc, want: 7},
		{name: "physical allocation", typ: profiling.TypeMemory, mode: profiling.ModePhysicalAlloc, want: 7},
		{name: "retained physical usage", typ: profiling.TypeMemory, mode: profiling.ModePhysicalUsage, want: 7},
		{name: "lock duration", typ: profiling.TypeLock, want: 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			pctx := &pcontext.ProfilerContext{Ctx: ctx, Type: tt.typ, Mode: tt.mode, Freq: 100, OutputFormat: output.FormatRemote}
			aggr, err := newNativeAggregator(pctx)
			if err != nil {
				t.Fatal(err)
			}
			if tt.typ == profiling.TypeLock {
				aggr.Aggregate(&lockSample{Process: processKey{PID: 12, Comm: "app"}, WaitNanoseconds: 7})
			} else {
				sample := &stackSample{Process: processKey{PID: 12, Comm: "app"}, Value: 10}
				aggr.Aggregate(sample)
				sample.Value = -3
				aggr.Aggregate(sample)
			}
			wantStart, wantDuration := window.Start.UnixNano(), window.Duration().Nanoseconds()
			if tt.mode == profiling.ModePhysicalUsage {
				wantStart, wantDuration = window.End.UnixNano(), 0
			}
			// A final snapshot must not use cancellation or serialization time.
			for range 2 {
				data, err := aggr.Snapshot(pctx, window)
				if err != nil {
					t.Fatal(err)
				}
				decoded := decodeWindowProfile(t, data)
				if decoded.TimeNanos != wantStart || decoded.DurationNanos != wantDuration {
					t.Fatalf("time/duration = %d/%d, want %d/%d", decoded.TimeNanos, decoded.DurationNanos, wantStart, wantDuration)
				}
				if len(decoded.Sample) != 1 || decoded.Sample[0].Value[0] != tt.want {
					t.Fatalf("sample values = %v, want [%d]", decoded.Sample, tt.want)
				}
			}
		})
	}
}

func TestNativeAggregatorRejectsUnknownCollectionWindow(t *testing.T) {
	valid := nativeTestCollectionWindow()
	for _, window := range []profiler.CollectionWindow{
		{},
		{Start: valid.Start},
		{End: valid.End},
		{Start: valid.End, End: valid.Start},
	} {
		pctx := &pcontext.ProfilerContext{Type: profiling.TypeCPU, Mode: profiling.ModeOnCPU, OutputFormat: output.FormatRemote}
		aggr, err := newNativeAggregator(pctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := aggr.Snapshot(pctx, window); err == nil {
			t.Fatalf("Snapshot accepted invalid window %v", window)
		}
	}
}

func TestExternalAggregatorsKeepOwnTiming(t *testing.T) {
	startedAt := time.Unix(1_600_000_000, 123)
	pctx := &pcontext.ProfilerContext{Ctx: context.Background(), Type: profiling.TypeCPU, Mode: profiling.ModeOnCPU, Freq: 100, OutputFormat: output.FormatRemote}
	for _, name := range []string{"java", "python"} {
		t.Run(name, func(t *testing.T) {
			var aggr aggregator.Aggregator
			if name == "java" {
				aggr = &javaAggregator{startedAt: startedAt}
			} else {
				aggr = &pythonAggregator{startedAt: startedAt, sampleRate: 100}
			}
			aggr.Aggregate(profiler.SampleOutput{PID: os.Getpid(), Output: "main;work 7"})
			data, err := aggr.Snapshot(pctx, nativeTestCollectionWindow())
			if err != nil {
				t.Fatal(err)
			}
			decoded := decodeWindowProfile(t, data)
			if decoded.TimeNanos != startedAt.UnixNano() || decoded.DurationNanos != 0 {
				t.Fatalf("time/duration = %d/%d, want unchanged %d/0", decoded.TimeNanos, decoded.DurationNanos, startedAt.UnixNano())
			}
		})
	}
}

func decodeWindowProfile(t *testing.T, data any) *pprof.Profile {
	t.Helper()
	result, ok := data.(*profiler.ProfileData)
	if !ok {
		t.Fatalf("profile type = %T, want *profiler.ProfileData", data)
	}
	wire, err := result.Profile.MarshalVT()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := pprof.ParseData(wire)
	if err != nil {
		t.Fatalf("parse serialized pprof: %v", err)
	}
	return decoded
}

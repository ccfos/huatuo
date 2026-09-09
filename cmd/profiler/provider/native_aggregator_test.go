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
	"bytes"
	"context"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"testing"

	"huatuo-bamai/internal/profiler"
	"huatuo-bamai/internal/profiler/aggregator"
	pcontext "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
	"huatuo-bamai/pkg/profiling"
)

func TestNativeAggregatorAggregatesLockTime(t *testing.T) {
	aggregator := &nativeAggregator{
		stackSamples: map[stackSampleKey]int64{},
		lockSamples:  map[string]*lockSample{},
	}
	record := &lockSample{
		Process:         processKey{PID: 12, Comm: "app"},
		LockAddress:     0xab,
		StackTrace:      symbolizedStackTrace{UserFrames: []string{"foo", "bar"}},
		WaitNanoseconds: 10,
		ContentionCount: 2,
	}

	aggregator.Aggregate(record)
	aggregator.Aggregate(record)

	requireSingleLockRecord(t, aggregator, 20, 4)
	_, sampleType, err := profileTypeOptions(&pcontext.ProfilerContext{Type: profiling.TypeLock})
	if err != nil {
		t.Fatalf("profileTypeOptions() error = %v", err)
	}
	if sampleType != profiler.ProfileTypeLockTimeSample {
		t.Fatalf("sample type = %q, want %q", sampleType, profiler.ProfileTypeLockTimeSample)
	}
}

func TestNativeAggregatorAggregatesStackSamplesWithoutMutatingInput(t *testing.T) {
	aggregator := &nativeAggregator{stackSamples: make(map[stackSampleKey]int64)}
	sample := &stackSample{
		Process: processKey{PID: 12, Comm: "app"},
		StackTrace: symbolizedStackTrace{
			UserFrames:   []string{"main", "work"},
			KernelFrames: []string{"entry_SYSCALL_64_after_hwframe"},
		},
		Value:    7,
		Category: "cpu",
	}

	aggregator.Aggregate(sample)
	aggregator.Aggregate(sample)

	if sample.Value != 7 {
		t.Fatalf("input sample value = %d, want 7", sample.Value)
	}
	requireSingleStackValue(t, aggregator, 14)
}

func TestNativeAggregatorSeparatesStackSampleIdentity(t *testing.T) {
	base := stackSample{
		Process: processKey{PID: 12, Comm: "app"},
		StackTrace: symbolizedStackTrace{
			UserFrames:   []string{"main", "work"},
			KernelFrames: []string{"entry_SYSCALL_64_after_hwframe"},
		},
		Value:    1,
		Category: "cpu",
	}
	tests := []struct {
		name  string
		right stackSample
	}{
		{
			name: "pid",
			right: stackSample{
				Process:    processKey{PID: 13, Comm: "app"},
				StackTrace: base.StackTrace,
				Value:      1,
				Category:   "cpu",
			},
		},
		{
			name: "comm",
			right: stackSample{
				Process:    processKey{PID: 12, Comm: "worker"},
				StackTrace: base.StackTrace,
				Value:      1,
				Category:   "cpu",
			},
		},
		{
			name: "category",
			right: stackSample{
				Process:    base.Process,
				StackTrace: base.StackTrace,
				Value:      1,
				Category:   "off-CPU blocked",
			},
		},
		{
			name: "user frames",
			right: stackSample{
				Process: base.Process,
				StackTrace: symbolizedStackTrace{
					UserFrames:   []string{"main", "other"},
					KernelFrames: base.StackTrace.KernelFrames,
				},
				Value:    1,
				Category: "cpu",
			},
		},
		{
			name: "kernel frames",
			right: stackSample{
				Process: base.Process,
				StackTrace: symbolizedStackTrace{
					UserFrames:   base.StackTrace.UserFrames,
					KernelFrames: []string{"do_syscall_64"},
				},
				Value:    1,
				Category: "cpu",
			},
		},
	}

	for index := range tests {
		tt := &tests[index]
		t.Run(tt.name, func(t *testing.T) {
			aggregator := &nativeAggregator{stackSamples: make(map[stackSampleKey]int64)}
			left := base
			right := tt.right
			aggregator.Aggregate(&left)
			aggregator.Aggregate(&right)
			if len(aggregator.stackSamples) != 2 {
				t.Fatalf("aggregated records = %d, want 2", len(aggregator.stackSamples))
			}
		})
	}
}

func TestNativeAggregatorSnapshotResolvesStackTraceIDs(t *testing.T) {
	aggregator := &nativeAggregator{stackSamples: make(map[stackSampleKey]int64)}
	aggregator.Aggregate(&stackSample{
		Process: processKey{PID: 12, Comm: "app"},
		StackTrace: symbolizedStackTrace{
			UserFrames:   []string{"main", "work"},
			KernelFrames: []string{"entry_SYSCALL_64_after_hwframe"},
		},
		Value:    7,
		Category: "cpu",
	})

	snapshot, err := aggregator.Snapshot(&pcontext.ProfilerContext{
		Type:         profiling.TypeMemory,
		OutputFormat: output.FormatRemote,
	})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	data, ok := snapshot.(*profiler.ProfileData)
	if !ok {
		t.Fatalf("Snapshot() type = %T, want *profiler.ProfileData", snapshot)
	}
	for _, frame := range []string{
		"process 12:app",
		"cpu",
		"main",
		"work",
		"entry_SYSCALL_64_after_hwframe",
	} {
		if !slices.Contains(data.Profile.StringTable, frame) {
			t.Errorf("Snapshot() string table does not contain %q", frame)
		}
	}
	if len(data.Profile.Sample) != 1 {
		t.Fatalf("Snapshot() samples = %d, want 1", len(data.Profile.Sample))
	}
	if got := data.Profile.Sample[0].Value; !slices.Equal(got, []int64{7}) {
		t.Fatalf("Snapshot() sample value = %v, want [7]", got)
	}
}

func TestNativeAggregatorPhysicalMemoryFiltersAfterAggregation(t *testing.T) {
	aggregator := &nativeAggregator{stackSamples: make(map[stackSampleKey]int64)}
	sample := &stackSample{
		Process:    processKey{PID: 12, Comm: "app"},
		StackTrace: symbolizedStackTrace{UserFrames: []string{"main"}},
		Value:      10,
	}
	aggregator.Aggregate(sample)
	sample.Value = -20
	aggregator.Aggregate(sample)

	snapshot, err := aggregator.Snapshot(&pcontext.ProfilerContext{
		Type:         profiling.TypeMemory,
		MemoryMode:   profiling.MemoryModePhysicalUsage,
		OutputFormat: output.FormatRemote,
	})
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	data, ok := snapshot.(*profiler.ProfileData)
	if !ok {
		t.Fatalf("Snapshot() type = %T, want *profiler.ProfileData", snapshot)
	}
	if len(data.Profile.Sample) != 0 {
		t.Fatalf("Snapshot() samples = %d, want 0", len(data.Profile.Sample))
	}
}

func TestNativeAggregatorSnapshotSurvivesResetAndNewSamples(t *testing.T) {
	tests := []struct {
		name        string
		typ         profiling.Type
		cpuMode     profiling.CPUMode
		memoryMode  profiling.MemoryMode
		profileType string
		scale       int64
	}{
		{
			name:        "on-CPU samples become nanoseconds",
			typ:         profiling.TypeCPU,
			cpuMode:     profiling.CPUModeOnCPU,
			profileType: profiler.ProfileTypeCpuSample,
			scale:       10_000_000,
		},
		{
			name:        "off-CPU nanoseconds stay unchanged",
			typ:         profiling.TypeCPU,
			cpuMode:     profiling.CPUModeOffCPU,
			profileType: profiler.ProfileTypeOffCpuSample,
			scale:       1,
		},
		{
			name:        "virtual allocation bytes stay unchanged",
			typ:         profiling.TypeMemory,
			memoryMode:  profiling.MemoryModeVirtualAlloc,
			profileType: profiler.ProfileTypeMemSample,
			scale:       1,
		},
		{
			name:        "physical allocation bytes stay unchanged",
			typ:         profiling.TypeMemory,
			memoryMode:  profiling.MemoryModePhysicalAlloc,
			profileType: profiler.ProfileTypeMemSample,
			scale:       1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pctx := &pcontext.ProfilerContext{
				Type:         tt.typ,
				CPUMode:      tt.cpuMode,
				MemoryMode:   tt.memoryMode,
				Freq:         100,
				OutputFormat: output.FormatRemote,
			}
			aggr, err := newNativeAggregator(pctx)
			if err != nil {
				t.Fatalf("newNativeAggregator() error = %v", err)
			}
			takeSnapshot := func() *profiler.ProfileData {
				t.Helper()
				snapshot, err := aggr.Snapshot(pctx)
				if err != nil {
					t.Fatalf("Snapshot() error = %v", err)
				}
				data, ok := snapshot.(*profiler.ProfileData)
				if !ok {
					t.Fatalf("Snapshot() type = %T, want *profiler.ProfileData", snapshot)
				}
				if data.ProfileType != tt.profileType {
					t.Fatalf("ProfileType = %q, want %q", data.ProfileType, tt.profileType)
				}
				return data
			}
			sample := stackSample{
				Process:    processKey{PID: 12, Comm: "app"},
				StackTrace: symbolizedStackTrace{UserFrames: []string{"main", "original"}},
				Value:      3,
			}
			aggr.Aggregate(&sample)
			pending := takeSnapshot()
			if len(pending.Profile.Sample) != 1 ||
				!slices.Equal(pending.Profile.Sample[0].Value, []int64{3 * tt.scale}) {
				t.Fatalf("pending samples = %v, want one sample with value %d", pending.Profile.Sample, 3*tt.scale)
			}
			before, err := pending.Profile.MarshalVT()
			if err != nil {
				t.Fatalf("marshal pending profile: %v", err)
			}

			aggr.Reset()
			// A pending profile must stay independent even when the interner reuses IDs.
			aggr.Aggregate(&stackSample{
				Process:    sample.Process,
				StackTrace: symbolizedStackTrace{UserFrames: []string{"main", "new"}},
				Value:      7,
			})
			sample.Value = 5
			aggr.Aggregate(&sample)
			next := takeSnapshot()
			var values []int64
			for _, sample := range next.Profile.Sample {
				if len(sample.Value) != 1 {
					t.Fatalf("next sample values = %v, want one value", sample.Value)
				}
				values = append(values, sample.Value[0])
			}
			slices.Sort(values)
			if want := []int64{5 * tt.scale, 7 * tt.scale}; !slices.Equal(values, want) {
				t.Fatalf("next sample values = %v, want %v", values, want)
			}
			for _, frame := range []string{"original", "new"} {
				if !slices.Contains(next.Profile.StringTable, frame) {
					t.Errorf("next profile is missing frame %q", frame)
				}
			}
			after, err := pending.Profile.MarshalVT()
			if err != nil {
				t.Fatalf("marshal pending profile after reset: %v", err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("pending profile changed after resetting and aggregating the next window")
			}
		})
	}
}

func TestSymbolizedStackTraceAppendToPreservesFrameNames(t *testing.T) {
	trace := symbolizedStackTrace{
		UserFrames:   []string{"generic::<[u8; 7]>", "main"},
		KernelFrames: []string{"entry_SYSCALL_64_after_hwframe"},
	}
	want := []string{
		"process 123:worker",
		"generic::<[u8; 7]>",
		"main",
		"entry_SYSCALL_64_after_hwframe",
	}

	got := trace.appendTo([]string{"process 123:worker"})
	if !slices.Equal(got, want) {
		t.Fatalf("appendTo() = %q, want %q", got, want)
	}
}

func TestNativeAggregatorPreservesStructuredFrameBoundaries(t *testing.T) {
	tests := []struct {
		name  string
		left  symbolizedStackTrace
		right symbolizedStackTrace
	}{
		{
			name:  "literal semicolon is not a frame separator",
			left:  symbolizedStackTrace{UserFrames: []string{"foo;bar"}},
			right: symbolizedStackTrace{UserFrames: []string{"foo", "bar"}},
		},
		{
			name:  "user and kernel sections remain distinct",
			left:  symbolizedStackTrace{UserFrames: []string{"same"}},
			right: symbolizedStackTrace{KernelFrames: []string{"same"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			aggregator := &nativeAggregator{stackSamples: make(map[stackSampleKey]int64)}
			left := &stackSample{Process: processKey{PID: 12, Comm: "app"}, StackTrace: tt.left}
			right := &stackSample{Process: processKey{PID: 12, Comm: "app"}, StackTrace: tt.right}
			aggregator.Aggregate(left)
			aggregator.Aggregate(right)
			if len(aggregator.stackSamples) != 2 {
				t.Fatalf("aggregated records = %d, want 2", len(aggregator.stackSamples))
			}
		})
	}
}

func TestNativeAggregatorResetClearsStackTraceState(t *testing.T) {
	aggregator := &nativeAggregator{
		stackSamples: make(map[stackSampleKey]int64),
		lockSamples:  make(map[string]*lockSample),
	}
	aggregator.Aggregate(&stackSample{
		Process:    processKey{PID: 12, Comm: "app"},
		StackTrace: symbolizedStackTrace{UserFrames: []string{"main"}},
		Value:      1,
	})

	aggregator.Reset()

	if len(aggregator.stackSamples) != 0 {
		t.Fatalf("aggregated records after Reset() = %d, want 0", len(aggregator.stackSamples))
	}
	if aggregator.stackTraces.heads != nil || aggregator.stackTraces.traces != nil {
		t.Fatal("stack trace state retained after Reset()")
	}
}

func TestBuildTreeItemPreservesRawFrameNames(t *testing.T) {
	trace := symbolizedStackTrace{
		UserFrames:   []string{"generic::<[u8; 7]>"},
		KernelFrames: []string{"kernel;frame"},
	}

	item := buildTreeItem([]string{"process 12:app"}, trace, 7)
	want := []string{"process 12:app", "generic::<[u8; 7]>", "kernel;frame"}
	if len(item.Stack) != len(want) {
		t.Fatalf("stack length = %d, want %d", len(item.Stack), len(want))
	}
	for index, frame := range item.Stack {
		if string(frame) != want[index] {
			t.Fatalf("stack[%d] = %q, want %q", index, frame, want[index])
		}
	}
	if item.Value != 7 {
		t.Fatalf("value = %d, want 7", item.Value)
	}
}

func TestUserStackCacheKeyIncludesPID(t *testing.T) {
	first := userStackCacheKey{PID: 100, StackID: 7}
	second := userStackCacheKey{PID: 200, StackID: 7}
	if first == second {
		t.Fatal("user stack cache key aliases the same stack ID across processes")
	}
}

func BenchmarkAppendStackFrames(b *testing.B) {
	prefixes := []string{"process 123:worker", "off-CPU blocked"}
	trace := symbolizedStackTrace{
		UserFrames:   []string{"runtime.goexit", "net/http.(*conn).serve", "main.handle"},
		KernelFrames: []string{"entry_SYSCALL_64_after_hwframe", "do_syscall_64"},
	}

	b.ReportAllocs()
	for b.Loop() {
		frames := trace.appendTo(prefixes[:len(prefixes):len(prefixes)])
		runtime.KeepAlive(frames)
	}
}

func BenchmarkNativeAggregatorAggregate(b *testing.B) {
	for _, depth := range []int{5, 32, 64} {
		b.Run(fmt.Sprintf("repeated/user=%d", depth), func(b *testing.B) {
			aggregator := &nativeAggregator{stackSamples: make(map[stackSampleKey]int64)}
			sample := &stackSample{
				Process: processKey{PID: 123, Comm: "worker"},
				StackTrace: symbolizedStackTrace{
					UserFrames: benchmarkStackFrames("user", depth),
				},
				Value: 1,
			}

			b.ReportAllocs()
			for b.Loop() {
				aggregator.Aggregate(sample)
			}
			runtime.KeepAlive(aggregator)
		})
	}

	b.Run("repeated/mixed=32+32", func(b *testing.B) {
		aggregator := &nativeAggregator{stackSamples: make(map[stackSampleKey]int64)}
		sample := &stackSample{
			Process: processKey{PID: 123, Comm: "worker"},
			StackTrace: symbolizedStackTrace{
				UserFrames:   benchmarkStackFrames("user", 32),
				KernelFrames: benchmarkStackFrames("kernel", 32),
			},
			Value: 1,
		}

		b.ReportAllocs()
		for b.Loop() {
			aggregator.Aggregate(sample)
		}
		runtime.KeepAlive(aggregator)
	})

	b.Run("unique/user=32/batch=1024", func(b *testing.B) {
		const sampleCount = 1024
		samples := make([]stackSample, sampleCount)
		for index := range samples {
			samples[index] = stackSample{
				Process: processKey{PID: 123, Comm: "worker"},
				StackTrace: symbolizedStackTrace{
					UserFrames: benchmarkStackFrames(strconv.Itoa(index), 32),
				},
				Value: 1,
			}
		}

		b.ReportAllocs()
		for b.Loop() {
			aggregator := &nativeAggregator{stackSamples: make(map[stackSampleKey]int64)}
			for index := range samples {
				aggregator.Aggregate(&samples[index])
			}
			runtime.KeepAlive(aggregator)
		}
	})
}

const nativePipelineBenchmarkBatchSize = 1024

type nativePipelineBenchmarkAggregator struct {
	*nativeAggregator
	batchDone chan struct{}
	records   int
}

func (a *nativePipelineBenchmarkAggregator) Aggregate(rec any) {
	a.nativeAggregator.Aggregate(rec)
	a.records++
	if a.records%nativePipelineBenchmarkBatchSize == 0 {
		a.batchDone <- struct{}{}
	}
}

func (*nativePipelineBenchmarkAggregator) Snapshot(*pcontext.ProfilerContext) (any, error) {
	// Export would measure serialization and storage instead of the queue hot path.
	return nil, nil
}

func BenchmarkNativeAggregatorPipeline(b *testing.B) {
	for _, depth := range []int{5, 32, 64} {
		b.Run(fmt.Sprintf("repeated/user=%d/batch=%d", depth, nativePipelineBenchmarkBatchSize), func(b *testing.B) {
			pctx := &pcontext.ProfilerContext{
				Ctx:          context.Background(),
				Type:         profiling.TypeCPU,
				CPUMode:      profiling.CPUModeOnCPU,
				Freq:         99,
				TracerID:     "native-pipeline-benchmark",
				OutputFormat: output.FormatRemote,
				IsOneShotAgg: true,
			}
			native, err := newNativeAggregator(pctx)
			if err != nil {
				b.Fatalf("newNativeAggregator() error = %v", err)
			}
			aggr := &nativePipelineBenchmarkAggregator{
				nativeAggregator: native,
				batchDone:        make(chan struct{}, 1),
			}
			pipe := aggregator.NewPipeline(pctx, aggr)
			pipe.Start()
			b.Cleanup(pipe.Stop)
			sample := &stackSample{
				Process: processKey{PID: 123, Comm: "worker"},
				StackTrace: symbolizedStackTrace{
					UserFrames: benchmarkStackFrames("user", depth),
				},
				Value: 1,
			}

			b.ReportAllocs()
			for b.Loop() {
				for range nativePipelineBenchmarkBatchSize {
					pipe.Enqueue(sample)
				}
				// Bound outstanding records below queue capacity and time their consumption.
				<-aggr.batchDone
			}
			pipe.Stop()
			want := b.N * nativePipelineBenchmarkBatchSize
			if aggr.records != want {
				b.Fatalf("consumed records = %d, want %d", aggr.records, want)
			}
			if len(native.stackSamples) != 1 {
				b.Fatalf("stack records = %d, want 1", len(native.stackSamples))
			}
			for _, value := range native.stackSamples {
				if value != int64(want) {
					b.Fatalf("aggregated value = %d, want %d", value, want)
				}
			}
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(want), "ns/record")
		})
	}
}

func benchmarkStackFrames(prefix string, depth int) []string {
	frames := make([]string, depth)
	for index := range frames {
		frames[index] = prefix + "-frame-" + strconv.Itoa(index)
	}
	return frames
}

func requireSingleStackValue(t *testing.T, aggregator *nativeAggregator, value int64) {
	t.Helper()
	if len(aggregator.stackSamples) != 1 {
		t.Fatalf("stack records = %d, want 1", len(aggregator.stackSamples))
	}
	for _, got := range aggregator.stackSamples {
		if got != value {
			t.Fatalf("stack value = %d, want %d", got, value)
		}
	}
}

func requireSingleLockRecord(t *testing.T, aggregator *nativeAggregator, waitTime uint64, contended uint32) {
	t.Helper()
	if len(aggregator.lockSamples) != 1 {
		t.Fatalf("lock records = %d, want 1", len(aggregator.lockSamples))
	}
	for _, record := range aggregator.lockSamples {
		if record.WaitNanoseconds != waitTime || record.ContentionCount != contended {
			t.Fatalf(
				"lock record = (wait=%d, contended=%d), want (wait=%d, contended=%d)",
				record.WaitNanoseconds,
				record.ContentionCount,
				waitTime,
				contended,
			)
		}
	}
}

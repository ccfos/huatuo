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
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"testing"

	"huatuo-bamai/internal/profiler"
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
		Mode:         profiling.ModePhysicalUsage,
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

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
	"errors"
	"maps"
	"sync"
	"testing"
	"time"

	"huatuo-bamai/internal/profiler"
	profctx "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
)

type uploadSample struct {
	stack string
	value int64
}

// Model the providers' independently locked Aggregate, Snapshot and Reset.
type uploadTestAggregator struct {
	mu         sync.Mutex
	samples    map[string]int64
	aggregated chan struct{}
	snapshots  int
	resets     int
}

func newUploadTestAggregator() *uploadTestAggregator {
	return &uploadTestAggregator{
		samples:    make(map[string]int64),
		aggregated: make(chan struct{}, 16),
	}
}

func (a *uploadTestAggregator) Aggregate(rec any) {
	sample := rec.(uploadSample)
	a.mu.Lock()
	a.samples[sample.stack] += sample.value
	a.mu.Unlock()
	if a.aggregated != nil {
		a.aggregated <- struct{}{}
	}
}

func (a *uploadTestAggregator) Snapshot(*profctx.ProfilerContext, profiler.CollectionWindow) (any, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.snapshots++
	if len(a.samples) == 0 {
		return nil, nil
	}
	return maps.Clone(a.samples), nil
}

func (a *uploadTestAggregator) Reset() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.resets++
	clear(a.samples)
}

func (*uploadTestAggregator) OutputFormatter() output.Formatter { return nil }

func startUploadConsumer(t *testing.T, a Aggregator) *Pipeline {
	t.Helper()
	p := NewPipeline(&profctx.ProfilerContext{Ctx: t.Context()}, a)
	p.windowStart = p.now()
	// Drive export explicitly so the test does not depend on ticker timing.
	p.wg.Add(1)
	go p.runDequeueAndAggregate()
	t.Cleanup(p.Stop)
	return p
}

func waitUploadSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for profile aggregation during upload")
	}
}

func TestPipelineUploadPreservesConcurrentSamples(t *testing.T) {
	a := newUploadTestAggregator()
	p := startUploadConsumer(t, a)
	p.Enqueue(uploadSample{stack: "existing", value: 3})
	waitUploadSignal(t, a.aggregated)

	uploading := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseUpload := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseUpload)
	finished := make(chan error, 1)
	var first map[string]int64
	go func() {
		finished <- p.uploadSnapshot(t.Context(), false, func(_ context.Context, data any) error {
			first = data.(map[string]int64)
			close(uploading)
			<-release
			return nil
		})
	}()
	waitUploadSignal(t, uploading)

	for _, sample := range []uploadSample{{stack: "existing", value: 5}, {stack: "new", value: 7}} {
		p.Enqueue(sample)
		waitUploadSignal(t, a.aggregated)
	}
	releaseUpload()
	if err := <-finished; err != nil {
		t.Fatalf("first upload: %v", err)
	}
	if !maps.Equal(first, map[string]int64{"existing": 3}) {
		t.Fatalf("first window = %v, want existing=3", first)
	}

	var second map[string]int64
	if err := p.uploadSnapshot(t.Context(), false, func(_ context.Context, data any) error {
		second = data.(map[string]int64)
		return nil
	}); err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if !maps.Equal(second, map[string]int64{"existing": 5, "new": 7}) {
		t.Fatalf("second window = %v, want existing=5 and new=7", second)
	}
	if got := p.overflowCount.Load(); got != 0 {
		t.Fatalf("queue overflow = %d, want 0", got)
	}
}

type snapshotBoundaryAggregator struct {
	*uploadTestAggregator
	checkExclusion func()
	afterSnapshot  func()
}

func (a *snapshotBoundaryAggregator) Aggregate(rec any) {
	a.checkExclusion()
	a.uploadTestAggregator.Aggregate(rec)
}

func (a *snapshotBoundaryAggregator) Snapshot(pctx *profctx.ProfilerContext, window profiler.CollectionWindow) (any, error) {
	data, err := a.uploadTestAggregator.Snapshot(pctx, window)
	a.checkExclusion()
	// The provider has released its own lock, exposing the Snapshot/Reset gap.
	a.afterSnapshot()
	return data, err
}

func (a *snapshotBoundaryAggregator) Reset() {
	a.checkExclusion()
	a.uploadTestAggregator.Reset()
}

func TestPipelineUploadSnapshotAndResetExcludeAggregation(t *testing.T) {
	a := &snapshotBoundaryAggregator{uploadTestAggregator: newUploadTestAggregator()}
	p := NewPipeline(&profctx.ProfilerContext{Ctx: t.Context()}, a)
	p.windowStart = p.now()
	// A completed send proves the consumer has received the concurrent record.
	p.queue = make(chan any)
	a.checkExclusion = func() {
		// Detect a missing boundary lock without relying on goroutine scheduling.
		if p.aggregationMu.TryLock() {
			p.aggregationMu.Unlock()
			t.Error("Aggregate, Snapshot and Reset must share pipeline exclusion")
		}
	}
	snapshotReady := make(chan struct{})
	release := make(chan struct{})
	var pauseOnce, releaseOnce sync.Once
	releaseSnapshot := func() { releaseOnce.Do(func() { close(release) }) }
	a.afterSnapshot = func() {
		pauseOnce.Do(func() {
			close(snapshotReady)
			<-release
		})
	}
	p.wg.Add(1)
	go p.runDequeueAndAggregate()
	t.Cleanup(p.Stop)
	t.Cleanup(releaseSnapshot)
	enqueue := func(sample uploadSample) {
		t.Helper()
		select {
		case p.queue <- sample:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out delivering sample to aggregation consumer")
		}
	}
	enqueue(uploadSample{stack: "existing", value: 3})
	waitUploadSignal(t, a.aggregated)

	finished := make(chan error, 1)
	uploadDone := make(chan struct{})
	var first map[string]int64
	go func() {
		defer close(uploadDone)
		finished <- p.uploadSnapshot(t.Context(), false, func(_ context.Context, data any) error {
			first = data.(map[string]int64)
			return nil
		})
	}()
	t.Cleanup(func() {
		releaseSnapshot()
		waitUploadSignal(t, uploadDone)
	})
	waitUploadSignal(t, snapshotReady)
	enqueue(uploadSample{stack: "existing", value: 5})
	releaseSnapshot()
	if err := <-finished; err != nil {
		t.Fatalf("first upload: %v", err)
	}
	waitUploadSignal(t, a.aggregated)
	enqueue(uploadSample{stack: "new", value: 7})
	waitUploadSignal(t, a.aggregated)

	var second map[string]int64
	if err := p.uploadSnapshot(t.Context(), false, func(_ context.Context, data any) error {
		second = data.(map[string]int64)
		return nil
	}); err != nil {
		t.Fatalf("second upload: %v", err)
	}
	if !maps.Equal(first, map[string]int64{"existing": 3}) ||
		!maps.Equal(second, map[string]int64{"existing": 5, "new": 7}) {
		t.Fatalf("windows = %v, %v; want existing=3 followed by existing=5 and new=7", first, second)
	}
}

func TestPipelineUploadRetriesFrozenWindow(t *testing.T) {
	a := newUploadTestAggregator()
	p := startUploadConsumer(t, a)
	p.Enqueue(uploadSample{stack: "existing", value: 3})
	waitUploadSignal(t, a.aggregated)
	uploadErr := errors.New("receiver unavailable")
	fail := func(_ context.Context, data any) error {
		if got := data.(map[string]int64); !maps.Equal(got, map[string]int64{"existing": 3}) {
			t.Errorf("retry window = %v, want only existing=3", got)
		}
		return uploadErr
	}
	if err := p.uploadSnapshot(t.Context(), false, fail); !errors.Is(err, uploadErr) {
		t.Fatalf("initial upload error = %v, want %v", err, uploadErr)
	}
	p.Enqueue(uploadSample{stack: "existing", value: 5})
	p.Enqueue(uploadSample{stack: "new", value: 7})
	waitUploadSignal(t, a.aggregated)
	waitUploadSignal(t, a.aggregated)
	for range 3 {
		if err := p.uploadSnapshot(t.Context(), false, fail); !errors.Is(err, uploadErr) {
			t.Fatalf("retry error = %v, want %v", err, uploadErr)
		}
	}

	var windows []map[string]int64
	send := func(_ context.Context, data any) error {
		windows = append(windows, data.(map[string]int64))
		return nil
	}
	if err := p.uploadSnapshot(t.Context(), false, send); err != nil {
		t.Fatalf("retry after recovery: %v", err)
	}
	if len(windows) != 1 || !maps.Equal(windows[0], map[string]int64{"existing": 3}) {
		t.Fatalf("recovered windows = %v, want one frozen window", windows)
	}
	if err := p.uploadSnapshot(t.Context(), false, send); err != nil {
		t.Fatalf("upload active window: %v", err)
	}
	if len(windows) != 2 || !maps.Equal(windows[1], map[string]int64{"existing": 5, "new": 7}) {
		t.Fatalf("windows = %v, want original and concurrent values separately", windows)
	}
	if a.snapshots != 2 || a.resets != 2 {
		t.Fatalf("snapshots/resets = %d/%d, want 2/2", a.snapshots, a.resets)
	}
}

func TestPipelineFinalUploadDrainsPendingAndActive(t *testing.T) {
	for _, failRetry := range []bool{false, true} {
		name := "recovered"
		if failRetry {
			name = "still unavailable"
		}
		t.Run(name, func(t *testing.T) {
			a := newUploadTestAggregator()
			p := startUploadConsumer(t, a)
			p.Enqueue(uploadSample{stack: "first", value: 3})
			waitUploadSignal(t, a.aggregated)
			uploadErr := errors.New("receiver unavailable")
			if err := p.uploadSnapshot(t.Context(), false, func(context.Context, any) error {
				return uploadErr
			}); !errors.Is(err, uploadErr) {
				t.Fatalf("initial upload error = %v", err)
			}
			p.Enqueue(uploadSample{stack: "last", value: 5})
			p.Stop()

			var windows []map[string]int64
			err := p.uploadSnapshot(t.Context(), true, func(_ context.Context, data any) error {
				windows = append(windows, data.(map[string]int64))
				if failRetry {
					return uploadErr
				}
				return nil
			})
			if len(windows) == 0 || !maps.Equal(windows[0], map[string]int64{"first": 3}) {
				t.Fatalf("final windows = %v, want frozen first window", windows)
			}
			if failRetry {
				if !errors.Is(err, uploadErr) || len(windows) != 1 || a.resets != 1 {
					t.Fatalf("failed final upload: error=%v windows=%v resets=%d", err, windows, a.resets)
				}
				if !maps.Equal(a.samples, map[string]int64{"last": 5}) {
					t.Fatalf("active samples = %v, want last=5", a.samples)
				}
				return
			}
			if err != nil || len(windows) != 2 || !maps.Equal(windows[1], map[string]int64{"last": 5}) {
				t.Fatalf("final upload: error=%v windows=%v, want both windows", err, windows)
			}
		})
	}
}

func TestPipelineUploadSnapshotFailureDoesNotReset(t *testing.T) {
	for _, snapshotErr := range []error{nil, errors.New("snapshot failed")} {
		name := "empty"
		if snapshotErr != nil {
			name = "error"
		}
		t.Run(name, func(t *testing.T) {
			a := NewMockAggregator(t)
			pctx := &profctx.ProfilerContext{}
			p := NewPipeline(pctx, a)
			start := time.Unix(100, 0)
			end := start.Add(time.Second)
			p.windowStart = start
			p.now = func() time.Time { return end }
			a.On("Snapshot", pctx, profiler.CollectionWindow{Start: start, End: end}).Return(nil, snapshotErr).Once()
			err := p.uploadSnapshot(t.Context(), false, func(context.Context, any) error {
				t.Error("upload called without a snapshot")
				return nil
			})
			if !errors.Is(err, snapshotErr) {
				t.Fatalf("upload error = %v, want %v", err, snapshotErr)
			}
			a.AssertNotCalled(t, "Reset")
		})
	}
}

func BenchmarkPipelineAggregation(b *testing.B) {
	a := newUploadTestAggregator()
	a.aggregated = nil
	p := NewPipeline(&profctx.ProfilerContext{TracerID: "benchmark"}, a)
	p.wg.Add(1)
	go p.runDequeueAndAggregate()
	sample := uploadSample{stack: "same stack", value: 1}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		p.queue <- sample
	}
	p.Stop()
}

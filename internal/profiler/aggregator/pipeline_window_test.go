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
	"sync/atomic"
	"testing"
	"time"

	"huatuo-bamai/internal/profiler"
	profctx "huatuo-bamai/internal/profiler/context"
	"huatuo-bamai/internal/profiler/output"
)

type pipelineWindowClock struct {
	mu   sync.Mutex
	time time.Time
}

func (c *pipelineWindowClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.time
}

func (c *pipelineWindowClock) set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.time = now
}

type pipelineWindowProfile struct {
	window  profiler.CollectionWindow
	samples map[string]int64
}

type pipelineWindowAggregator struct {
	*uploadTestAggregator
	windows     []profiler.CollectionWindow
	snapshotErr error
}

func newPipelineWindowAggregator() *pipelineWindowAggregator {
	a := newUploadTestAggregator()
	a.aggregated = nil
	return &pipelineWindowAggregator{uploadTestAggregator: a}
}

func (a *pipelineWindowAggregator) Snapshot(pctx *profctx.ProfilerContext, window profiler.CollectionWindow) (any, error) {
	a.windows = append(a.windows, window)
	if a.snapshotErr != nil {
		return nil, a.snapshotErr
	}
	data, err := a.uploadTestAggregator.Snapshot(pctx, window)
	if err != nil || data == nil {
		return nil, err
	}
	return &pipelineWindowProfile{window: window, samples: data.(map[string]int64)}, nil
}

func newPipelineWindowTest(t *testing.T) (*Pipeline, *pipelineWindowAggregator, *pipelineWindowClock) {
	t.Helper()
	a := newPipelineWindowAggregator()
	c := &pipelineWindowClock{time: time.Unix(100, 0)}
	p := NewPipeline(&profctx.ProfilerContext{Ctx: t.Context()}, a)
	p.now = c.now
	// Tests drive the export worker explicitly instead of waiting for tickers.
	p.windowStart = c.now()
	t.Cleanup(p.Stop)
	return p, a, c
}

func assertPipelineWindow(t *testing.T, got profiler.CollectionWindow, start, end time.Time) {
	t.Helper()
	if !got.Start.Equal(start) || !got.End.Equal(end) {
		t.Fatalf("collection window = [%s, %s], want [%s, %s]", got.Start, got.End, start, end)
	}
}

func TestPipelineWindowStartsAtStart(t *testing.T) {
	formatter := NewFormatter(t)
	formatter.On("IsEmpty").Return(true).Once()
	a := NewMockAggregator(t)
	a.On("OutputFormatter").Return(formatter).Once()
	p := NewPipeline(&profctx.ProfilerContext{
		Ctx:          t.Context(),
		OutputFormat: output.FormatCollapsed,
		IsOneShotAgg: true,
	}, a)
	clock := &pipelineWindowClock{time: time.Unix(100, 0)}
	p.now = clock.now
	if !p.windowStart.IsZero() {
		t.Fatalf("constructor set collection start to %s", p.windowStart)
	}

	start := clock.now().Add(time.Hour)
	clock.set(start)
	p.Start()
	t.Cleanup(p.Stop)
	clock.set(start.Add(time.Second))
	p.Start()
	end := start.Add(2 * time.Second)
	clock.set(end)
	p.Stop()
	clock.set(end.Add(time.Hour))
	p.Stop()
	assertPipelineWindow(t, profiler.CollectionWindow{Start: p.windowStart, End: p.stoppedAt}, start, end)
}

func TestPipelineWindowFinalWorkerAfterCancellation(t *testing.T) {
	for _, oneShot := range []bool{false, true} {
		name := "periodic"
		if oneShot {
			name = "one shot"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			pctx := &profctx.ProfilerContext{
				Ctx:          ctx,
				OutputFormat: output.FormatRemote,
				IsOneShotAgg: oneShot,
				AggrInterval: 3600,
			}
			a := NewMockAggregator(t)
			clock := &pipelineWindowClock{time: time.Unix(100, 0)}
			start := clock.now()
			end := start.Add(3 * time.Second)
			a.On("Aggregate", "accepted").Once()
			a.On("Snapshot", pctx, profiler.CollectionWindow{Start: start, End: end}).Return(nil, nil).Once()
			p := NewPipeline(pctx, a)
			p.now = clock.now
			p.Start()
			t.Cleanup(p.Stop)
			p.Enqueue("accepted")
			cancel()
			clock.set(end)
			p.Stop()
		})
	}
}

func TestPipelineWindowConsecutiveAndPartialFinal(t *testing.T) {
	p, a, clock := newPipelineWindowTest(t)
	start := clock.now()
	var profiles []*pipelineWindowProfile
	send := func(_ context.Context, data any) error {
		profiles = append(profiles, data.(*pipelineWindowProfile))
		return nil
	}
	for i := 1; i <= 2; i++ {
		p.aggregate(uploadSample{stack: "sample", value: int64(i)})
		clock.set(start.Add(time.Duration(i) * 10 * time.Second))
		if err := p.uploadSnapshot(t.Context(), false, send); err != nil {
			t.Fatal(err)
		}
	}
	p.aggregate(uploadSample{stack: "tail", value: 3})
	end := start.Add(23 * time.Second)
	clock.set(end)
	p.Stop()
	clock.set(end.Add(time.Hour))
	if err := p.uploadSnapshot(t.Context(), true, send); err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 3 || a.resets != 3 {
		t.Fatalf("profiles/resets = %d/%d, want 3/3", len(profiles), a.resets)
	}
	assertPipelineWindow(t, profiles[0].window, start, start.Add(10*time.Second))
	assertPipelineWindow(t, profiles[1].window, start.Add(10*time.Second), start.Add(20*time.Second))
	assertPipelineWindow(t, profiles[2].window, start.Add(20*time.Second), end)
	if got := profiles[2].samples; !maps.Equal(got, map[string]int64{"tail": 3}) {
		t.Fatalf("final samples = %v, want tail=3", got)
	}
}

func TestPipelineWindowRetryAndSlowFinalSend(t *testing.T) {
	p, a, clock := newPipelineWindowTest(t)
	start := clock.now()
	firstEnd := start.Add(10 * time.Second)
	p.aggregate(uploadSample{stack: "first", value: 1})
	clock.set(firstEnd)
	uploadErr := errors.New("receiver unavailable")
	var pending *pipelineWindowProfile
	if err := p.uploadSnapshot(t.Context(), false, func(_ context.Context, data any) error {
		pending = data.(*pipelineWindowProfile)
		// A receiver delay cannot prevent collecting the next window.
		p.aggregate(uploadSample{stack: "next", value: 2})
		clock.set(start.Add(time.Hour))
		return uploadErr
	}); !errors.Is(err, uploadErr) {
		t.Fatalf("first upload error = %v, want %v", err, uploadErr)
	}
	assertPipelineWindow(t, pending.window, start, firstEnd)
	clock.set(start.Add(2 * time.Hour))
	if err := p.uploadSnapshot(t.Context(), false, func(_ context.Context, data any) error {
		if data != pending {
			t.Error("retry replaced the frozen profile")
		}
		return uploadErr
	}); !errors.Is(err, uploadErr) {
		t.Fatalf("retry error = %v, want %v", err, uploadErr)
	}
	if len(a.windows) != 1 {
		t.Fatalf("snapshot count while pending = %d, want 1", len(a.windows))
	}
	end := start.Add(3 * time.Hour)
	clock.set(end)
	p.Stop()
	clock.set(start.Add(4 * time.Hour))
	var profiles []*pipelineWindowProfile
	if err := p.uploadSnapshot(t.Context(), true, func(_ context.Context, data any) error {
		profiles = append(profiles, data.(*pipelineWindowProfile))
		clock.set(start.Add(5 * time.Hour))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 || profiles[0] != pending {
		t.Fatalf("final profiles = %v, want frozen and active windows", profiles)
	}
	assertPipelineWindow(t, profiles[0].window, start, firstEnd)
	assertPipelineWindow(t, profiles[1].window, firstEnd, end)
	if got := profiles[1].samples; !maps.Equal(got, map[string]int64{"next": 2}) {
		t.Fatalf("active samples = %v, want next=2", got)
	}
}

func TestPipelineWindowEmptyAndSnapshotError(t *testing.T) {
	p, a, clock := newPipelineWindowTest(t)
	start := clock.now()
	emptyEnd := start.Add(10 * time.Second)
	clock.set(emptyEnd)
	unexpectedSend := func(context.Context, any) error {
		t.Error("sent a profile without a successful nonempty snapshot")
		return nil
	}
	if err := p.uploadSnapshot(t.Context(), false, unexpectedSend); err != nil {
		t.Fatal(err)
	}
	if !p.windowStart.Equal(emptyEnd) || a.resets != 0 {
		t.Fatalf("empty window start/resets = %s/%d, want %s/0", p.windowStart, a.resets, emptyEnd)
	}
	p.aggregate(uploadSample{stack: "retained", value: 1})
	snapshotErr := errors.New("invalid snapshot")
	a.snapshotErr = snapshotErr
	clock.set(start.Add(20 * time.Second))
	if err := p.uploadSnapshot(t.Context(), false, unexpectedSend); !errors.Is(err, snapshotErr) {
		t.Fatalf("snapshot error = %v, want %v", err, snapshotErr)
	}
	if !p.windowStart.Equal(emptyEnd) || a.resets != 0 {
		t.Fatalf("failed snapshot advanced start/reset: %s/%d", p.windowStart, a.resets)
	}
	a.snapshotErr = nil
	end := start.Add(30 * time.Second)
	clock.set(end)
	var profile *pipelineWindowProfile
	if err := p.uploadSnapshot(t.Context(), false, func(_ context.Context, data any) error {
		profile = data.(*pipelineWindowProfile)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertPipelineWindow(t, profile.window, emptyEnd, end)
	if !maps.Equal(profile.samples, map[string]int64{"retained": 1}) || a.resets != 1 {
		t.Fatalf("recovered samples/resets = %v/%d, want retained=1/1", profile.samples, a.resets)
	}
}

type pipelineBlockedWindowAggregator struct {
	*pipelineWindowAggregator
	once    sync.Once
	entered chan struct{}
	release chan struct{}
}

func (a *pipelineBlockedWindowAggregator) Aggregate(rec any) {
	a.once.Do(func() {
		close(a.entered)
		<-a.release
	})
	a.pipelineWindowAggregator.Aggregate(rec)
}

func TestPipelineWindowStopFreezesBeforeQueueDrain(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "normal"
		if canceled {
			name = "canceled"
		}
		t.Run(name, func(t *testing.T) {
			a := &pipelineBlockedWindowAggregator{
				pipelineWindowAggregator: newPipelineWindowAggregator(),
				entered:                  make(chan struct{}),
				release:                  make(chan struct{}),
			}
			p := startUploadConsumer(t, a)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			p.pctx.Ctx = ctx
			clock := &pipelineWindowClock{time: time.Unix(100, 0)}
			p.now = clock.now
			start := clock.now()
			p.windowStart = start
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(a.release) }) }
			t.Cleanup(release)
			p.Enqueue(uploadSample{stack: "first", value: 1})
			waitUploadSignal(t, a.entered)
			p.Enqueue(uploadSample{stack: "queued", value: 2})
			if canceled {
				cancel()
			}
			end := start.Add(3 * time.Second)
			clock.set(end)
			stopped := make(chan struct{})
			go func() {
				p.Stop()
				close(stopped)
			}()
			waitUploadSignal(t, p.stopCh)
			clock.set(end.Add(time.Hour))
			p.Enqueue(uploadSample{stack: "ignored", value: 4})
			release()
			waitUploadSignal(t, stopped)
			var profile *pipelineWindowProfile
			if err := p.uploadSnapshot(ctx, true, func(_ context.Context, data any) error {
				profile = data.(*pipelineWindowProfile)
				clock.set(end.Add(2 * time.Hour))
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			assertPipelineWindow(t, profile.window, start, end)
			if !maps.Equal(profile.samples, map[string]int64{"first": 1, "queued": 2}) {
				t.Fatalf("final samples = %v, want both accepted records", profile.samples)
			}
		})
	}
}

func TestPipelineWindowStoppedTickerWaitsForFinalDrain(t *testing.T) {
	p, a, clock := newPipelineWindowTest(t)
	start := clock.now()
	p.aggregate(uploadSample{stack: "aggregated", value: 1})
	p.Enqueue(uploadSample{stack: "queued", value: 2})
	end := start.Add(time.Second)
	clock.set(end)
	p.Stop()
	clock.set(end.Add(time.Hour))
	if err := p.uploadSnapshot(t.Context(), false, func(context.Context, any) error {
		t.Error("a ready ticker exported before the final drain")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(a.windows) != 0 || !p.windowStart.Equal(start) {
		t.Fatalf("stopped ticker split the final window: snapshots=%v start=%s", a.windows, p.windowStart)
	}
	// Model the remaining queue drain before the final export worker runs.
	p.aggregate(<-p.queue)
	var profile *pipelineWindowProfile
	if err := p.uploadSnapshot(t.Context(), true, func(_ context.Context, data any) error {
		profile = data.(*pipelineWindowProfile)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertPipelineWindow(t, profile.window, start, end)
	if !maps.Equal(profile.samples, map[string]int64{"aggregated": 1, "queued": 2}) {
		t.Fatalf("final samples = %v, want aggregated=1 and queued=2", profile.samples)
	}
}

func TestPipelineWindowBoundaryClockExcludesStop(t *testing.T) {
	p, _, clock := newPipelineWindowTest(t)
	start := clock.now()
	boundary := start.Add(time.Second)
	end := start.Add(2 * time.Second)
	entered := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	var calls atomic.Int32
	p.now = func() time.Time {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
			return boundary
		}
		return end
	}
	result := make(chan time.Time, 1)
	go func() {
		at, _ := p.snapshotEnd()
		result <- at
	}()
	waitUploadSignal(t, entered)
	if p.enqueueMutex.TryLock() {
		p.enqueueMutex.Unlock()
		t.Error("boundary clock is not protected against Stop")
	}
	stopped := make(chan struct{})
	go func() {
		p.Stop()
		close(stopped)
	}()
	unblock()
	if at := <-result; !at.Equal(boundary) {
		t.Fatalf("in-flight boundary = %s, want %s", at, boundary)
	}
	waitUploadSignal(t, stopped)
	at, isStopped := p.snapshotEnd()
	if !isStopped || !at.Equal(end) || calls.Load() != 2 {
		t.Fatalf("stopped boundary = %s/%t, clock calls=%d; want %s/true and 2", at, isStopped, calls.Load(), end)
	}
}

func BenchmarkPipelineWindowBoundary(b *testing.B) {
	p := NewPipeline(&profctx.ProfilerContext{TracerID: "benchmark"}, nil)
	p.windowStart = time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		end, _ := p.snapshotEnd()
		p.windowStart = end
	}
}

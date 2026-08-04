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

package collector

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/ioobserve/health"
	"huatuo-bamai/pkg/types"
)

type fakeIOHealthMDWatcher struct {
	startErr error
	changes  chan health.MDChange
	stop     chan error
	ctx      context.Context
}

type fakeIOHealthPerfReader struct {
	ctx context.Context
}

func (r *fakeIOHealthPerfReader) ReadInto(any) error {
	<-r.ctx.Done()
	return types.ErrExitByCancelCtx
}

func (r *fakeIOHealthPerfReader) ReadBatch(func() any) ([]any, error) {
	return nil, errors.New("unexpected batch read")
}

func (r *fakeIOHealthPerfReader) Close() error {
	return nil
}

type fakeIOHealthRuntimeBPF struct {
	bpf.BPF
}

func (b *fakeIOHealthRuntimeBPF) AttachWithOptions([]bpf.AttachOption) error {
	return nil
}

func (b *fakeIOHealthRuntimeBPF) DetachProgram(string) error {
	return nil
}

func (b *fakeIOHealthRuntimeBPF) EventPipeByName(
	ctx context.Context,
	_ string,
	_ uint32,
) (bpf.PerfEventReader, error) {
	return &fakeIOHealthPerfReader{ctx: ctx}, nil
}

func (b *fakeIOHealthRuntimeBPF) Close() error {
	return nil
}

func newFakeIOHealthMDWatcher() *fakeIOHealthMDWatcher {
	return &fakeIOHealthMDWatcher{
		changes: make(chan health.MDChange, 1),
		stop:    make(chan error, 1),
	}
}

func (w *fakeIOHealthMDWatcher) Start(ctx context.Context) error {
	if w.startErr != nil {
		return w.startErr
	}
	w.ctx = ctx
	return nil
}

func (w *fakeIOHealthMDWatcher) Wait() error {
	select {
	case err := <-w.stop:
		return err
	case <-w.ctx.Done():
		return nil
	}
}

func (w *fakeIOHealthMDWatcher) Changes() <-chan health.MDChange {
	return w.changes
}

func waitIOHealthAttempt(t *testing.T, attempts <-chan int, want int) {
	t.Helper()
	select {
	case got := <-attempts:
		if got != want {
			t.Fatalf("watcher attempt = %d, want %d", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for watcher attempt %d", want)
	}
}

func TestIOHealthMDSupervisorRetriesStartAndRuntimeFailures(t *testing.T) {
	watchers := []*fakeIOHealthMDWatcher{
		{startErr: errors.New("incomplete baseline")},
		newFakeIOHealthMDWatcher(),
		newFakeIOHealthMDWatcher(),
	}
	collector := newIOHealthCollector(t.TempDir(), filepath.Join(t.TempDir(), "mdstat"))
	attempts := make(chan int, len(watchers))
	next := 0
	collector.newMDWatcher = func(string, string) ioHealthMDWatcher {
		next++
		attempts <- next
		return watchers[next-1]
	}
	saved := make(chan types.IOHealthEvent, 1)
	collector.saveEvent = func(_ time.Time, event types.IOHealthEvent) error {
		saved <- event
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	retry := make(chan time.Time, 2)
	done := make(chan struct{})
	go func() {
		collector.superviseMDWatcher(
			ctx,
			&recordingEvidenceSubmitter{accept: true},
			retry,
		)
		close(done)
	}()

	waitIOHealthAttempt(t, attempts, 1)
	retry <- time.Now()
	waitIOHealthAttempt(t, attempts, 2)
	watchers[1].stop <- errors.New("poll failed")
	retry <- time.Now()
	waitIOHealthAttempt(t, attempts, 3)
	watchers[2].changes <- health.MDChange{
		Array:      "md0",
		Field:      health.MDFieldSyncAction,
		OldState:   "idle",
		NewState:   "recover",
		ObservedAt: time.Unix(1, 2),
	}
	select {
	case event := <-saved:
		if event.Type != ioHealthTypeMDSyncAction || event.Array != "md0" {
			t.Fatalf("saved MD event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for MD event after restart")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("MD supervisor did not stop after cancellation")
	}
}

func TestIOHealthFinishMDWatcherDrainsChanges(t *testing.T) {
	collector := newIOHealthCollector(t.TempDir(), filepath.Join(t.TempDir(), "mdstat"))
	changes := make(chan health.MDChange, 1)
	changes <- health.MDChange{
		Array:      "md0",
		Field:      health.MDFieldSyncAction,
		OldState:   "idle",
		NewState:   "recover",
		ObservedAt: time.Unix(1, 2),
	}

	saved := make(chan types.IOHealthEvent, 1)
	collector.saveEvent = func(_ time.Time, event types.IOHealthEvent) error {
		saved <- event
		return nil
	}
	err := collector.finishMDWatcher(
		errors.New("poll failed"),
		changes,
		&recordingEvidenceSubmitter{accept: true},
	)
	if err == nil || err.Error() != "poll failed" {
		t.Fatalf("watcher error = %v, want poll failed", err)
	}
	select {
	case event := <-saved:
		if event.Type != ioHealthTypeMDSyncAction {
			t.Fatalf("saved drained MD event = %+v", event)
		}
	default:
		t.Fatal("buffered MD event was not drained")
	}
}

func TestIOHealthStartKeepsMDActiveAcrossBPFLoadFailures(t *testing.T) {
	collector := newIOHealthCollector(t.TempDir(), filepath.Join(t.TempDir(), "mdstat"))
	watcher := newFakeIOHealthMDWatcher()
	mdStarted := make(chan struct{}, 1)
	mdAttempts := 0
	collector.newMDWatcher = func(string, string) ioHealthMDWatcher {
		mdAttempts++
		mdStarted <- struct{}{}
		return watcher
	}
	saved := make(chan types.IOHealthEvent, 1)
	collector.saveEvent = func(_ time.Time, event types.IOHealthEvent) error {
		saved <- event
		return nil
	}
	bpfAttempts := make(chan int, 2)
	releaseFirstLoad := make(chan struct{})
	attempt := 0
	loadBPF := func(string, map[string]any) (bpf.BPF, error) {
		attempt++
		bpfAttempts <- attempt
		if attempt == 1 {
			<-releaseFirstLoad
			return nil, errors.New("BPF unavailable")
		}
		return &fakeIOHealthRuntimeBPF{}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- collector.start(ctx, loadBPF, time.Millisecond)
	}()

	select {
	case <-mdStarted:
	case <-time.After(time.Second):
		t.Fatal("MD watcher did not start")
	}
	watcher.changes <- health.MDChange{
		Array:      "md0",
		Field:      health.MDFieldSyncAction,
		OldState:   "idle",
		NewState:   "recover",
		ObservedAt: time.Unix(1, 2),
	}
	select {
	case event := <-saved:
		if event.Type != ioHealthTypeMDSyncAction {
			t.Fatalf("saved MD event = %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("MD watcher did not consume changes during BPF failures")
	}
	waitIOHealthAttempt(t, bpfAttempts, 1)
	close(releaseFirstLoad)
	waitIOHealthAttempt(t, bpfAttempts, 2)
	select {
	case err := <-done:
		t.Fatalf("collector stopped after BPF failure: %v", err)
	default:
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("collector stop error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not stop after cancellation")
	}
	if mdAttempts != 1 {
		t.Fatalf("MD watcher starts = %d, want 1", mdAttempts)
	}
}

func TestIOHealthCancellationDuringBPFLoadIsNormal(t *testing.T) {
	collector := newIOHealthCollector(t.TempDir(), filepath.Join(t.TempDir(), "mdstat"))
	watcher := newFakeIOHealthMDWatcher()
	collector.newMDWatcher = func(string, string) ioHealthMDWatcher {
		return watcher
	}
	loadStarted := make(chan struct{})
	releaseLoad := make(chan struct{})
	loadBPF := func(string, map[string]any) (bpf.BPF, error) {
		close(loadStarted)
		<-releaseLoad
		return nil, errors.New("BPF unavailable")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- collector.start(ctx, loadBPF, time.Hour)
	}()
	<-loadStarted
	cancel()
	close(releaseLoad)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("collector stop error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("collector did not stop after cancellation")
	}
}

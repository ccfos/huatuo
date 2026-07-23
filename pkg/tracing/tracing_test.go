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

package tracing

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	pkgtypes "huatuo-bamai/pkg/types"
)

type starterStub struct {
	startFunc func(context.Context) error
}

func (s *starterStub) Start(ctx context.Context) error {
	return s.startFunc(ctx)
}

func TestNewEventRunner(t *testing.T) {
	starter := &starterStub{
		startFunc: func(context.Context) error { return pkgtypes.ErrNotSupported },
	}
	runner := newEventRunner("cpu", starter, 2*time.Second, FlagTracing)

	got := runner.snapshot()
	want := LifecycleSnapshot{
		Name:            "cpu",
		RestartInterval: 2,
		Roles:           FlagTracing,
	}
	if got != want {
		t.Errorf("newEventRunner().snapshot() = %+v, want %+v", got, want)
	}
}

func TestEventRunnerLifecycle(t *testing.T) {
	started := make(chan struct{})
	starter := &starterStub{
		startFunc: func(ctx context.Context) error {
			close(started)
			<-ctx.Done()

			return pkgtypes.ErrExitByCancelCtx
		},
	}
	runner := newEventRunner("trace-2026", starter, time.Hour, FlagTracing)

	if err := runner.start(t.Context()); err != nil {
		t.Fatalf("eventRunner.start() error = %v, want nil", err)
	}
	<-started

	if err := runner.start(t.Context()); !errors.Is(err, ErrTracerAlreadyRunning) {
		t.Errorf("eventRunner.start() error = %v, want ErrTracerAlreadyRunning", err)
	}

	runningSnapshot := runner.snapshot()
	if !runningSnapshot.IsRunning {
		t.Error("eventRunner.snapshot().IsRunning = false, want true")
	}

	if err := runner.stop(t.Context()); err != nil {
		t.Fatalf("eventRunner.stop() error = %v, want nil", err)
	}

	stoppedSnapshot := runner.snapshot()
	if stoppedSnapshot.IsRunning {
		t.Error("eventRunner.snapshot().IsRunning = true, want false")
	}
	if stoppedSnapshot.RunCount != 1 {
		t.Errorf("eventRunner.snapshot().RunCount = %d, want 1", stoppedSnapshot.RunCount)
	}
	if err := runner.stop(t.Context()); !errors.Is(err, ErrTracerNotRunning) {
		t.Errorf("eventRunner.stop() error = %v, want ErrTracerNotRunning", err)
	}
}

func TestEventRunnerRejectsCanceledStartContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	runner := newEventRunner(
		"canceled",
		&starterStub{startFunc: func(context.Context) error { return nil }},
		time.Second,
		FlagTracing,
	)

	if err := runner.start(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("eventRunner.start() error = %v, want context.Canceled", err)
	}
}

func TestEventRunnerStopHonorsContext(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runner := newEventRunner(
		"slow-stop",
		&starterStub{
			startFunc: func(context.Context) error {
				close(started)
				<-release

				return nil
			},
		},
		time.Hour,
		FlagTracing,
	)

	if err := runner.start(t.Context()); err != nil {
		t.Fatalf("eventRunner.start() error = %v, want nil", err)
	}
	<-started

	runner.mu.RLock()
	done := runner.done
	runner.mu.RUnlock()

	stopCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := runner.stop(stopCtx); !errors.Is(err, context.Canceled) {
		t.Errorf("eventRunner.stop() error = %v, want context.Canceled", err)
	}

	close(release)
	<-done
}

func TestWaitForRunnerPrefersCompletion(t *testing.T) {
	done := make(chan struct{})
	close(done)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	for range 100 {
		if err := waitForRunner(ctx, "completed", done); err != nil {
			t.Fatalf("waitForRunner() error = %v, want nil", err)
		}
	}
}

func TestEventRunnerStopsDuringRestartInterval(t *testing.T) {
	firstRun := make(chan struct{})
	var runCount atomic.Int32
	starter := &starterStub{
		startFunc: func(context.Context) error {
			if runCount.Add(1) == 1 {
				close(firstRun)
			}

			return nil
		},
	}
	runner := newEventRunner("trace-restart", starter, time.Hour, FlagTracing)

	if err := runner.start(t.Context()); err != nil {
		t.Fatalf("eventRunner.start() error = %v, want nil", err)
	}
	<-firstRun

	if err := runner.stop(t.Context()); err != nil {
		t.Fatalf("eventRunner.stop() error = %v, want nil", err)
	}
	if got := runCount.Load(); got != 1 {
		t.Errorf("starter.Start() calls = %d, want 1", got)
	}
}

func TestEventRunnerNotSupportedStops(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	starter := &starterStub{
		startFunc: func(context.Context) error {
			close(started)
			<-release

			return pkgtypes.ErrNotSupported
		},
	}
	runner := newEventRunner("unsupported", starter, time.Hour, FlagTracing)

	if err := runner.start(t.Context()); err != nil {
		t.Fatalf("eventRunner.start() error = %v, want nil", err)
	}
	<-started

	runner.mu.RLock()
	done := runner.done
	runner.mu.RUnlock()
	close(release)
	<-done

	got := runner.snapshot()
	if got.IsRunning {
		t.Error("eventRunner.snapshot().IsRunning = true, want false")
	}
	if got.RunCount != 1 {
		t.Errorf("eventRunner.snapshot().RunCount = %d, want 1", got.RunCount)
	}
}

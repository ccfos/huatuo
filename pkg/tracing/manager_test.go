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
	"testing"
	"time"

	pkgtypes "huatuo-bamai/pkg/types"
)

type stopMatchingError struct {
	err error
}

func (e *stopMatchingError) Error() string {
	return e.err.Error()
}

func (e *stopMatchingError) Unwrap() error {
	return e.err
}

func (e *stopMatchingError) Is(target error) bool {
	return target == pkgtypes.ErrNotSupported
}

func TestNewManager(t *testing.T) {
	resetRegisterState()
	t.Cleanup(resetRegisterState)

	RegisterEventTracing("trace_only", func() (*EventTracingAttr, error) {
		return &EventTracingAttr{
			Flag:     FlagTracing,
			Interval: 1,
			TracingData: &starterStub{
				startFunc: func(context.Context) error {
					return pkgtypes.ErrNotSupported
				},
			},
		}, nil
	})
	RegisterEventTracing("metric_only", func() (*EventTracingAttr, error) {
		return &EventTracingAttr{
			Flag:        FlagMetric,
			TracingData: struct{}{},
		}, nil
	})

	manager, err := NewManager(nil)
	if err != nil {
		t.Fatalf("NewManager() error = %v, want nil", err)
	}
	if got := len(manager.runners); got != 1 {
		t.Fatalf("len(Manager.runners) = %d, want 1", got)
	}
	if _, ok := manager.runners["trace_only"]; !ok {
		t.Error(`Manager.runners["trace_only"] is missing`)
	}
}

func TestNewManagerRejectsInvalidTracer(t *testing.T) {
	tests := []struct {
		name        string
		interval    int
		tracingData any
	}{
		{
			name:        "missing starter",
			interval:    1,
			tracingData: struct{}{},
		},
		{
			name:     "non-positive restart interval",
			interval: 0,
			tracingData: &starterStub{
				startFunc: func(context.Context) error { return nil },
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetRegisterState()
			t.Cleanup(resetRegisterState)

			RegisterEventTracing("invalid", func() (*EventTracingAttr, error) {
				return &EventTracingAttr{
					Flag:        FlagTracing,
					Interval:    tt.interval,
					TracingData: tt.tracingData,
				}, nil
			})

			_, err := NewManager(nil)
			if !errors.Is(err, ErrInvalidTracer) {
				t.Errorf("NewManager() error = %v, want ErrInvalidTracer", err)
			}
		})
	}
}

func TestManagerLifecycle(t *testing.T) {
	started := make(chan struct{})
	runner := newEventRunner(
		"trace-2026",
		&starterStub{
			startFunc: func(ctx context.Context) error {
				close(started)
				<-ctx.Done()

				return pkgtypes.ErrExitByCancelCtx
			},
		},
		time.Hour,
		FlagTracing,
	)
	manager := &Manager{
		runners: map[string]*eventRunner{"trace-2026": runner},
	}

	if err := manager.StartByName(t.Context(), "missing"); !errors.Is(err, ErrTracerNotFound) {
		t.Errorf("Manager.StartByName() error = %v, want ErrTracerNotFound", err)
	}
	if err := manager.StartByName(t.Context(), "trace-2026"); err != nil {
		t.Fatalf("Manager.StartByName() error = %v, want nil", err)
	}
	<-started

	snapshot := manager.Snapshots()["trace-2026"]
	if !snapshot.IsRunning {
		t.Error("Manager.Snapshots()[trace-2026].IsRunning = false, want true")
	}

	if err := manager.StopByName(t.Context(), "trace-2026"); err != nil {
		t.Fatalf("Manager.StopByName() error = %v, want nil", err)
	}
	if err := manager.StopByName(t.Context(), "trace-2026"); !errors.Is(err, ErrTracerNotRunning) {
		t.Errorf("Manager.StopByName() error = %v, want ErrTracerNotRunning", err)
	}
}

func TestManagerCloseWaitsForAllRunners(t *testing.T) {
	const runnerCount = 2

	started := make(chan struct{}, runnerCount)
	canceled := make(chan struct{}, runnerCount)
	release := make(chan struct{})
	runners := make(map[string]*eventRunner, runnerCount)
	for _, name := range []string{"first", "second"} {
		runners[name] = newEventRunner(
			name,
			&starterStub{
				startFunc: func(ctx context.Context) error {
					started <- struct{}{}
					<-ctx.Done()
					canceled <- struct{}{}
					<-release

					return pkgtypes.ErrExitByCancelCtx
				},
			},
			time.Hour,
			FlagTracing,
		)
	}
	manager := &Manager{runners: runners}

	if err := manager.Start(t.Context()); err != nil {
		t.Fatalf("Manager.Start() error = %v, want nil", err)
	}
	for range runnerCount {
		<-started
	}

	closeErr := make(chan error, 1)
	go func() {
		closeErr <- manager.Close(t.Context())
	}()

	for range runnerCount {
		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("Manager.Close() did not cancel all runners before waiting")
		}
	}
	close(release)

	if err := <-closeErr; err != nil {
		t.Fatalf("Manager.Close() error = %v, want nil", err)
	}
	if err := manager.StartByName(t.Context(), "first"); !errors.Is(err, ErrManagerClosed) {
		t.Errorf("Manager.StartByName() error = %v, want ErrManagerClosed", err)
	}
	if err := manager.Close(t.Context()); err != nil {
		t.Errorf("second Manager.Close() error = %v, want nil", err)
	}
}

func TestManagerCloseReportsShutdownErrors(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")
	tests := []struct {
		name       string
		startErr   func(context.Context) error
		wantErr    error
		wantCancel bool
	}{
		{
			name: "cleanup error",
			startErr: func(context.Context) error {
				return cleanupErr
			},
			wantErr: cleanupErr,
		},
		{
			name: "cancellation joined with cleanup error",
			startErr: func(ctx context.Context) error {
				return errors.Join(ctx.Err(), cleanupErr)
			},
			wantErr:    cleanupErr,
			wantCancel: true,
		},
		{
			name: "pure context cancellation",
			startErr: func(ctx context.Context) error {
				return ctx.Err()
			},
		},
		{
			name: "pure project stop sentinel",
			startErr: func(context.Context) error {
				return pkgtypes.ErrExitByCancelCtx
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			started := make(chan struct{})
			runner := newEventRunner(
				"shutdown",
				&starterStub{
					startFunc: func(ctx context.Context) error {
						close(started)
						<-ctx.Done()
						return tt.startErr(ctx)
					},
				},
				time.Hour,
				FlagTracing,
			)
			manager := &Manager{
				runners: map[string]*eventRunner{"shutdown": runner},
			}

			if err := manager.Start(t.Context()); err != nil {
				t.Fatalf("Manager.Start() error = %v, want nil", err)
			}
			<-started

			err := manager.Close(t.Context())
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Manager.Close() error = %v, want %v", err, tt.wantErr)
			}
			if got := errors.Is(err, context.Canceled); got != tt.wantCancel {
				t.Errorf(
					"errors.Is(Manager.Close(), context.Canceled) = %t, want %t",
					got,
					tt.wantCancel,
				)
			}
		})
	}
}

func TestManagerStopByNameReportsShutdownError(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")
	started := make(chan struct{})
	runner := newEventRunner(
		"stop",
		&starterStub{
			startFunc: func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				return cleanupErr
			},
		},
		time.Hour,
		FlagTracing,
	)
	manager := &Manager{
		runners: map[string]*eventRunner{"stop": runner},
	}

	if err := manager.Start(t.Context()); err != nil {
		t.Fatalf("Manager.Start() error = %v, want nil", err)
	}
	<-started
	if err := manager.StopByName(t.Context(), "stop"); !errors.Is(err, cleanupErr) {
		t.Errorf("Manager.StopByName() error = %v, want %v", err, cleanupErr)
	}
	if err := manager.StopByName(t.Context(), "stop"); !errors.Is(err, ErrTracerNotRunning) {
		t.Errorf("second Manager.StopByName() error = %v, want ErrTracerNotRunning", err)
	}
}

func TestManagerRequiresCompletedErrorClaimBeforeRestart(t *testing.T) {
	firstErr := errors.New("first cleanup failed")
	secondErr := errors.New("second cleanup failed")
	errs := []error{firstErr, secondErr}
	nextErr := 0
	runner := newEventRunner(
		"completed",
		&starterStub{
			startFunc: func(context.Context) error {
				err := errs[nextErr]
				nextErr++
				return errors.Join(pkgtypes.ErrNotSupported, err)
			},
		},
		time.Hour,
		FlagTracing,
	)
	manager := &Manager{
		runners: map[string]*eventRunner{"completed": runner},
	}

	if err := manager.StartByName(t.Context(), "completed"); err != nil {
		t.Fatalf("first Manager.StartByName() error = %v, want nil", err)
	}
	waitForLatestGeneration(t, runner)
	if err := manager.StartByName(t.Context(), "completed"); !errors.Is(err, ErrTracerRunErrorPending) {
		t.Fatalf("second Manager.StartByName() error = %v, want pending run error", err)
	}
	if err := manager.StopByName(t.Context(), "completed"); !errors.Is(err, firstErr) {
		t.Fatalf("Manager.StopByName() error = %v, want %v", err, firstErr)
	}
	if err := manager.StartByName(t.Context(), "completed"); err != nil {
		t.Fatalf("third Manager.StartByName() error = %v, want nil", err)
	}
	waitForLatestGeneration(t, runner)

	err := manager.Close(t.Context())
	if !errors.Is(err, secondErr) {
		t.Errorf("Manager.Close() error = %v, want %v", err, secondErr)
	}
	if err := manager.Close(t.Context()); err != nil {
		t.Errorf("second Manager.Close() error = %v, want nil", err)
	}
}

func TestManagerCompletedStopSentinelsRemainBounded(t *testing.T) {
	const generations = 2048

	runner := newEventRunner(
		"unsupported",
		&starterStub{
			startFunc: func(context.Context) error {
				return pkgtypes.ErrNotSupported
			},
		},
		time.Hour,
		FlagTracing,
	)
	manager := &Manager{
		runners: map[string]*eventRunner{"unsupported": runner},
	}

	for generation := 0; generation < generations; generation++ {
		if err := manager.StartByName(t.Context(), "unsupported"); err != nil {
			t.Fatalf("Manager.StartByName(%d) error = %v", generation, err)
		}
		waitForLatestGeneration(t, runner)

		runner.mu.RLock()
		retained := len(runner.completions)
		runner.mu.RUnlock()
		if retained > 1 {
			t.Fatalf("retained completions after generation %d = %d, want at most 1", generation, retained)
		}
	}
}

func TestManagerCloseIgnoresCompletedStopSentinel(t *testing.T) {
	runner := newEventRunner(
		"unsupported",
		&starterStub{
			startFunc: func(context.Context) error {
				return pkgtypes.ErrNotSupported
			},
		},
		time.Hour,
		FlagTracing,
	)
	manager := &Manager{
		runners: map[string]*eventRunner{"unsupported": runner},
	}

	if err := manager.Start(t.Context()); err != nil {
		t.Fatalf("Manager.Start() error = %v, want nil", err)
	}
	waitForLatestGeneration(t, runner)
	if err := manager.Close(t.Context()); err != nil {
		t.Errorf("Manager.Close() error = %v, want nil", err)
	}
}

func TestManagerCloseRetainsErrorWrappedByStopMatchingError(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")
	runner := newEventRunner(
		"wrapped",
		&starterStub{
			startFunc: func(context.Context) error {
				return &stopMatchingError{err: cleanupErr}
			},
		},
		time.Hour,
		FlagTracing,
	)
	manager := &Manager{
		runners: map[string]*eventRunner{"wrapped": runner},
	}

	if err := manager.Start(t.Context()); err != nil {
		t.Fatalf("Manager.Start() error = %v, want nil", err)
	}
	waitForLatestGeneration(t, runner)
	if err := manager.Close(t.Context()); !errors.Is(err, cleanupErr) {
		t.Errorf("Manager.Close() error = %v, want %v", err, cleanupErr)
	}
}

func TestManagerCloseRetainsCompletionAfterWaitTimeout(t *testing.T) {
	cleanupErr := errors.New("delayed cleanup failed")
	started := make(chan struct{})
	release := make(chan struct{})
	runner := newEventRunner(
		"delayed",
		&starterStub{
			startFunc: func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				<-release
				return cleanupErr
			},
		},
		time.Hour,
		FlagTracing,
	)
	manager := &Manager{
		runners: map[string]*eventRunner{"delayed": runner},
	}

	if err := manager.Start(t.Context()); err != nil {
		t.Fatalf("Manager.Start() error = %v, want nil", err)
	}
	<-started

	stopCtx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := manager.Close(stopCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Manager.Close() error = %v, want context.Canceled", err)
	}

	close(release)
	waitForLatestGeneration(t, runner)
	if err := manager.Close(t.Context()); !errors.Is(err, cleanupErr) {
		t.Errorf("second Manager.Close() error = %v, want %v", err, cleanupErr)
	}
}

func TestManagerCloseAndStopConsumeTerminalErrorOnce(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")
	started := make(chan struct{})
	runner := newEventRunner(
		"concurrent",
		&starterStub{
			startFunc: func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				return cleanupErr
			},
		},
		time.Hour,
		FlagTracing,
	)
	manager := &Manager{
		runners: map[string]*eventRunner{"concurrent": runner},
	}

	if err := manager.Start(t.Context()); err != nil {
		t.Fatalf("Manager.Start() error = %v, want nil", err)
	}
	<-started

	results := make(chan error, 2)
	go func() {
		results <- manager.Close(t.Context())
	}()
	go func() {
		results <- manager.StopByName(t.Context(), "concurrent")
	}()

	seen := 0
	for range 2 {
		err := <-results
		if errors.Is(err, ErrTracerNotRunning) {
			t.Errorf("concurrent stop error = %v, want nil or cleanup error", err)
		}
		if errors.Is(err, cleanupErr) {
			seen++
		}
	}
	if seen != 1 {
		t.Errorf("cleanup error returned %d times, want 1", seen)
	}
	if err := manager.StopByName(t.Context(), "concurrent"); err != nil {
		t.Errorf("StopByName() after Close error = %v, want nil", err)
	}
}

func waitForLatestGeneration(t *testing.T, runner *eventRunner) {
	t.Helper()

	runner.mu.RLock()
	completion := runner.completions[len(runner.completions)-1]
	runner.mu.RUnlock()
	<-completion.done
}

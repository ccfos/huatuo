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

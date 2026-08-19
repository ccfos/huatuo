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

package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"huatuo-bamai/internal/toolstream"
)

func TestRunCleanupsPreservesDependenciesAfterTimeout(t *testing.T) {
	called := make([]string, 0, 7)
	steps := []cleanupStep{
		{
			name: "pidfile",
			cleanup: func(context.Context) error {
				called = append(called, "pidfile")
				return nil
			},
		},
		{
			name: "cgroup",
			cleanup: func(context.Context) error {
				called = append(called, "cgroup")
				return nil
			},
		},
		{
			name:     "storage",
			requires: dependencyTracingStopped | dependencyToolstreamStopped,
			cleanup: func(context.Context) error {
				called = append(called, "storage")
				return nil
			},
		},
		{
			name:     "bpf",
			requires: dependencyTracingStopped,
			cleanup: func(context.Context) error {
				called = append(called, "bpf")
				return nil
			},
		},
		{
			name:     "pod",
			requires: dependencyTracingStopped,
			cleanup: func(context.Context) error {
				called = append(called, "pod")
				return nil
			},
		},
		{
			name:               "toolstream",
			requires:           dependencyTracingStopped,
			blocksOnIncomplete: dependencyToolstreamStopped,
			cleanup: func(context.Context) error {
				called = append(called, "toolstream")
				return nil
			},
		},
		{
			name:               "tracing",
			blocksOnIncomplete: dependencyTracingStopped,
			cleanup: func(ctx context.Context) error {
				called = append(called, "tracing")
				<-ctx.Done()
				return ctx.Err()
			},
		},
	}

	err := runCleanups(t.Context(), steps, time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runCleanups() error = %v, want deadline exceeded", err)
	}
	if want := []string{"tracing", "cgroup", "pidfile"}; !slices.Equal(called, want) {
		t.Fatalf("cleanup calls = %v, want %v", called, want)
	}
}

func TestRunSetupStepJoinsSetupAndRollbackErrors(t *testing.T) {
	setupErr := errors.New("setup failed")
	rollbackErr := errors.New("rollback failed")
	cleanups := []cleanupStep{{
		name: "existing",
		cleanup: func(context.Context) error {
			return rollbackErr
		},
	}}
	step := daemonSetupStep{
		name: "next",
		setup: func(*Daemon) (func(context.Context) error, error) {
			return nil, setupErr
		},
	}

	err := runSetupStep(
		t.Context(),
		&Daemon{},
		&cleanups,
		step,
		time.Second,
	)
	if !errors.Is(err, setupErr) {
		t.Errorf("runSetupStep() error = %v, want setup error", err)
	}
	if !errors.Is(err, rollbackErr) {
		t.Errorf("runSetupStep() error = %v, want rollback error", err)
	}
	if !strings.Contains(err.Error(), "existing cleanup") {
		t.Errorf("runSetupStep() error = %q, want cleanup step name", err)
	}
}

func TestRunCleanupsContinuesAfterCompletedError(t *testing.T) {
	wantErr := errors.New("tracing stopped with an error")
	called := make([]string, 0, 2)
	steps := []cleanupStep{
		{
			name:     "storage",
			requires: dependencyToolstreamStopped,
			cleanup: func(context.Context) error {
				called = append(called, "storage")
				return nil
			},
		},
		{
			name: "tracing",
			cleanup: func(context.Context) error {
				called = append(called, "tracing")
				return wantErr
			},
		},
	}

	err := runCleanups(t.Context(), steps, time.Second)
	if !errors.Is(err, wantErr) {
		t.Fatalf("runCleanups() error = %v, want %v", err, wantErr)
	}
	if want := []string{"tracing", "storage"}; !slices.Equal(called, want) {
		t.Fatalf("cleanup calls = %v, want %v", called, want)
	}
}

func TestRunCleanupsIgnoresParentCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	called := false
	err := runCleanups(ctx, []cleanupStep{{
		name: "storage",
		cleanup: func(cleanupCtx context.Context) error {
			called = true
			return cleanupCtx.Err()
		},
	}}, time.Second)
	if err != nil {
		t.Fatalf("runCleanups() error = %v, want nil", err)
	}
	if !called {
		t.Fatal("cleanup was not called")
	}
}

func TestRunCleanupsContinuesWhenToolstreamForcedCloseIsSafe(t *testing.T) {
	called := make([]string, 0, 2)
	steps := []cleanupStep{
		{
			name: "storage",
			cleanup: func(context.Context) error {
				called = append(called, "storage")
				return nil
			},
		},
		{
			name: "toolstream",
			cleanup: func(ctx context.Context) error {
				called = append(called, "toolstream")
				<-ctx.Done()
				return closeToolstream(ctx, &fakeToolstreamServer{drainErr: ctx.Err()})
			},
		},
	}

	err := runCleanups(t.Context(), steps, time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runCleanups() error = %v, want deadline exceeded", err)
	}
	if want := []string{"toolstream", "storage"}; !slices.Equal(called, want) {
		t.Fatalf("cleanup calls = %v, want %v", called, want)
	}
}

func TestRunCleanupsStopsWhileToolstreamHandlerIsActive(t *testing.T) {
	called := make([]string, 0, 3)
	steps := []cleanupStep{
		{
			name: "cgroup",
			cleanup: func(context.Context) error {
				called = append(called, "cgroup")
				return nil
			},
		},
		{
			name:     "storage",
			requires: dependencyToolstreamStopped,
			cleanup: func(context.Context) error {
				called = append(called, "storage")
				return nil
			},
		},
		{
			name: "toolstream",
			cleanup: func(ctx context.Context) error {
				called = append(called, "toolstream")
				return closeToolstream(ctx, &fakeToolstreamServer{
					closeErr: toolstream.ErrHandlersActive,
				})
			},
		},
	}

	err := runCleanups(t.Context(), steps, time.Second)
	if !errors.Is(err, toolstream.ErrHandlersActive) {
		t.Fatalf("runCleanups() error = %v, want ErrHandlersActive", err)
	}
	if want := []string{"toolstream", "cgroup"}; !slices.Equal(called, want) {
		t.Fatalf("cleanup calls = %v, want %v", called, want)
	}
}

func TestRunCleanupsRetriesToolstreamBeforeStorage(t *testing.T) {
	called := make([]string, 0, 2)
	retryStarted := make(chan struct{})
	handlerReleased := make(chan struct{})
	srv := &fakeToolstreamServer{
		drainErrs: []error{
			errors.Join(toolstream.ErrDrainTimeout, context.DeadlineExceeded),
			nil,
		},
		closeErrs: []error{toolstream.ErrHandlersActive, nil},
		onDrain: func(_ context.Context, call int) {
			if call != 1 {
				return
			}
			close(retryStarted)
			<-handlerReleased
		},
	}
	go func() {
		<-retryStarted
		close(handlerReleased)
	}()
	steps := []cleanupStep{
		{
			name:     "storage",
			requires: dependencyToolstreamStopped,
			cleanup: func(context.Context) error {
				called = append(called, "storage")
				return nil
			},
		},
		{
			name:               "toolstream",
			blocksOnIncomplete: dependencyToolstreamStopped,
			cleanup: func(ctx context.Context) error {
				called = append(called, "toolstream")
				return closeToolstream(ctx, srv)
			},
		},
	}

	if err := runCleanups(t.Context(), steps, time.Minute); err != nil {
		t.Fatalf("runCleanups() error = %v, want recovered shutdown", err)
	}
	if want := []string{"toolstream", "storage"}; !slices.Equal(called, want) {
		t.Fatalf("cleanup calls = %v, want %v", called, want)
	}
}

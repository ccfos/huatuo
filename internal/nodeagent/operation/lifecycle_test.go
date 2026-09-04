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

package operation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerCompletesNaturalLifecycle(t *testing.T) {
	manager := newTestManager(t, testConfig())
	waitRelease := make(chan struct{})
	executor := &fakeExecutor{waitFn: func() error {
		<-waitRelease
		return nil
	}}
	created, _, err := manager.Start(StartRequest{
		RequestID: "request",
		Kind:      KindProfiling,
		Executor:  executor,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if created.Status != StatusPending || created.StartedAt != nil {
		t.Fatalf("created operation = %+v, want pending without started time", created)
	}
	running := waitForStatus(t, manager, KindProfiling, "request", StatusRunning)
	if running.StartedAt == nil || running.FinishedAt != nil {
		t.Fatalf("running operation times = (%v, %v), want started only", running.StartedAt, running.FinishedAt)
	}
	close(waitRelease)
	completed := waitForStatus(t, manager, KindProfiling, "request", StatusTerminal)
	if completed.Terminal == nil || completed.Terminal.Outcome != OutcomeCompleted || completed.FinishedAt == nil {
		t.Fatalf("completed operation = %+v, want finish without failure", completed)
	}

	wantCalls := []string{"start", "wait", fmt.Sprintf("finalize:%d", FinalizePublish)}
	if got := executor.callSequence(); !equalStrings(got, wantCalls) {
		t.Errorf("executor calls = %v, want %v", got, wantCalls)
	}
	manager.mu.RLock()
	activeCount := manager.activeCount
	execution := manager.operations["request"].execution
	manager.mu.RUnlock()
	if activeCount != 0 || execution != nil {
		t.Errorf("terminal resources = (active %d, execution %v), want released", activeCount, execution)
	}
}

func TestManagerClassifiesStartFailures(t *testing.T) {
	tests := []struct {
		name       string
		start      func(context.Context) error
		wantReason FailureReason
	}{
		{
			name: "executor error",
			start: func(context.Context) error {
				return errors.New("executor start failed")
			},
			wantReason: FailureReasonExecutionStartFailed,
		},
		{
			name: "launch timeout",
			start: func(ctx context.Context) error {
				<-ctx.Done()
				return fmt.Errorf("launch: %w", ctx.Err())
			},
			wantReason: FailureReasonLaunchTimeout,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := testConfig()
			config.Lifecycle.LaunchTimeout = 10 * time.Millisecond
			manager := newTestManager(t, config)
			executor := &fakeExecutor{startFn: test.start}
			_, _, err := manager.Start(StartRequest{
				RequestID: "request",
				Kind:      KindProfiling,
				Executor:  executor,
			})
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			operation := waitForStatus(t, manager, KindProfiling, "request", StatusTerminal)
			if operation.Terminal == nil || operation.Terminal.Reason != test.wantReason {
				t.Fatalf("terminal = %+v, want reason %s", operation.Terminal, test.wantReason)
			}
			_, waitCalls, stopCalls, finalizeCalls := executor.counts()
			if waitCalls != 0 || stopCalls != 0 || finalizeCalls != 0 {
				t.Errorf("post-start calls = (wait %d, stop %d, finalize %d), want zero", waitCalls, stopCalls, finalizeCalls)
			}
		})
	}
}

func TestManagerStopsPendingLaunch(t *testing.T) {
	manager := newTestManager(t, testConfig())
	startEntered := make(chan struct{})
	executor := &fakeExecutor{startFn: func(ctx context.Context) error {
		close(startEntered)
		<-ctx.Done()
		return fmt.Errorf("start canceled: %w", ctx.Err())
	}}
	_, _, err := manager.Start(StartRequest{
		RequestID: "request",
		Kind:      KindProfiling,
		Executor:  executor,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	stopping, initiated, err := manager.StopByID("request")
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !initiated || stopping.Status != StatusStopping {
		t.Fatalf("Stop() = (%+v, %t), want initiated stopping", stopping, initiated)
	}
	waitClosed(t, startEntered, "pending Start")
	waitForStatus(t, manager, KindProfiling, "request", StatusTerminal)
	_, waitCalls, stopCalls, finalizeCalls := executor.counts()
	if waitCalls != 0 || stopCalls != 0 || finalizeCalls != 0 {
		t.Errorf("pending stop calls = (wait %d, stop %d, finalize %d), want zero", waitCalls, stopCalls, finalizeCalls)
	}
}

func TestManagerKeepsStartErrorAfterStopIntent(t *testing.T) {
	manager := newTestManager(t, testConfig())
	startEntered := make(chan struct{})
	executor := &fakeExecutor{startFn: func(ctx context.Context) error {
		close(startEntered)
		<-ctx.Done()
		return errors.New("independent start failure")
	}}
	_, _, err := manager.Start(StartRequest{
		RequestID: "request",
		Kind:      KindProfiling,
		Executor:  executor,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitClosed(t, startEntered, "executor Start")
	if _, initiated, err := manager.StopByID("request"); err != nil || !initiated {
		t.Fatalf("Stop() = (_, %t, %v), want initiated", initiated, err)
	}
	operation := waitForStatus(t, manager, KindProfiling, "request", StatusTerminal)
	if operation.Terminal == nil || operation.Terminal.Reason != FailureReasonExecutionStartFailed {
		t.Fatalf("terminal = %+v, want execution_start_failed", operation.Terminal)
	}
}

func TestManagerStopsLaunchThatWinsCancellationRace(t *testing.T) {
	manager := newTestManager(t, testConfig())
	startEntered := make(chan struct{})
	stopCalled := make(chan struct{})
	executor := &fakeExecutor{
		startFn: func(ctx context.Context) error {
			close(startEntered)
			<-ctx.Done()
			return nil
		},
		waitFn: func() error {
			<-stopCalled
			return ErrStopped
		},
		stopFn: func(context.Context) error {
			close(stopCalled)
			return nil
		},
	}
	_, _, err := manager.Start(StartRequest{
		RequestID: "request",
		Kind:      KindProfiling,
		Executor:  executor,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitClosed(t, startEntered, "executor Start")
	if _, initiated, err := manager.StopByID("request"); err != nil || !initiated {
		t.Fatalf("Stop() = (_, %t, %v), want initiated", initiated, err)
	}
	waitForStatus(t, manager, KindProfiling, "request", StatusTerminal)
	_, waitCalls, stopCalls, finalizeCalls := executor.counts()
	if waitCalls != 1 || stopCalls != 1 || finalizeCalls != 1 {
		t.Errorf("calls = (wait %d, stop %d, finalize %d), want one each", waitCalls, stopCalls, finalizeCalls)
	}
	if modes := executor.modes(); len(modes) != 1 || modes[0] != FinalizeDiscard {
		t.Errorf("finalize modes = %v, want discard", modes)
	}
}

func TestManagerSuccessfulStopOverridesWaitExitError(t *testing.T) {
	manager := newTestManager(t, testConfig())
	stopCalled := make(chan struct{})
	executor := &fakeExecutor{
		waitFn: func() error {
			<-stopCalled
			return errors.New("tool handled termination with a non-zero exit")
		},
		stopFn: func(context.Context) error {
			close(stopCalled)
			return nil
		},
	}
	_, _, err := manager.Start(StartRequest{
		RequestID: "request",
		Kind:      KindProfiling,
		Executor:  executor,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForStatus(t, manager, KindProfiling, "request", StatusRunning)
	if _, initiated, err := manager.StopByID("request"); err != nil || !initiated {
		t.Fatalf("Stop() = (_, %t, %v), want initiated", initiated, err)
	}
	operation := waitForStatus(t, manager, KindProfiling, "request", StatusTerminal)
	if operation.Terminal == nil || operation.Terminal.Outcome != OutcomeStopped {
		t.Fatalf("stopped operation terminal = %+v, want stopped", operation.Terminal)
	}
	if modes := executor.modes(); len(modes) != 1 || modes[0] != FinalizeDiscard {
		t.Fatalf("finalize modes = %v, want discard", modes)
	}
}

func TestManagerMergesConcurrentStops(t *testing.T) {
	manager := newTestManager(t, testConfig())
	stopCalled := make(chan struct{})
	var stopOnce sync.Once
	executor := &fakeExecutor{
		waitFn: func() error {
			<-stopCalled
			return ErrStopped
		},
		stopFn: func(context.Context) error {
			stopOnce.Do(func() { close(stopCalled) })
			return nil
		},
	}
	_, _, err := manager.Start(StartRequest{
		RequestID: "request",
		Kind:      KindProfiling,
		Executor:  executor,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForStatus(t, manager, KindProfiling, "request", StatusRunning)

	const stopCount = 20
	var (
		wg             sync.WaitGroup
		initiatedCount atomic.Int32
	)
	wg.Add(stopCount)
	for range stopCount {
		go func() {
			defer wg.Done()
			_, initiated, stopErr := manager.StopByID("request")
			if stopErr != nil {
				t.Errorf("Stop() error = %v", stopErr)
			}
			if initiated {
				initiatedCount.Add(1)
			}
		}()
	}
	wg.Wait()
	waitForStatus(t, manager, KindProfiling, "request", StatusTerminal)
	if got := initiatedCount.Load(); got != 1 {
		t.Errorf("initiated Stop count = %d, want 1", got)
	}
	_, _, stopCalls, _ := executor.counts()
	if stopCalls != 1 {
		t.Errorf("executor Stop calls = %d, want 1", stopCalls)
	}
}

func TestManagerRejectsStopDuringFinalization(t *testing.T) {
	manager := newTestManager(t, testConfig())
	finalizeEntered := make(chan struct{})
	finalizeRelease := make(chan struct{})
	executor := &fakeExecutor{finalizeFn: func(context.Context, FinalizeMode) error {
		close(finalizeEntered)
		<-finalizeRelease
		return nil
	}}
	_, _, err := manager.Start(StartRequest{
		RequestID: "request",
		Kind:      KindProfiling,
		Executor:  executor,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitClosed(t, finalizeEntered, "Finalize")
	waitForFinalizing(t, manager, "request")
	operation, initiated, err := manager.StopByID("request")
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if initiated || operation.Status != StatusRunning {
		t.Fatalf("Stop() = (%+v, %t), want unchanged running", operation, initiated)
	}
	_, _, stopCalls, _ := executor.counts()
	if stopCalls != 0 {
		t.Errorf("executor Stop calls = %d, want 0", stopCalls)
	}
	close(finalizeRelease)
	waitForStatus(t, manager, KindProfiling, "request", StatusTerminal)
}

func TestManagerClassifiesExecutionAndFinalizationFailures(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*fakeExecutor)
		stop       bool
		wantReason FailureReason
		wantMode   FinalizeMode
	}{
		{
			name: "wait failure has priority over finalization",
			configure: func(executor *fakeExecutor) {
				executor.waitFn = func() error { return errors.New("wait failed") }
				executor.finalizeFn = func(context.Context, FinalizeMode) error {
					return errors.New("finalize failed")
				}
			},
			wantReason: FailureReasonExecutionFailed,
			wantMode:   FinalizeDiscard,
		},
		{
			name: "unexpected stopped wait",
			configure: func(executor *fakeExecutor) {
				executor.waitFn = func() error { return ErrStopped }
			},
			wantReason: FailureReasonExecutionFailed,
			wantMode:   FinalizeDiscard,
		},
		{
			name: "finalization failure",
			configure: func(executor *fakeExecutor) {
				executor.finalizeFn = func(context.Context, FinalizeMode) error {
					return errors.New("finalize failed")
				}
			},
			wantReason: FailureReasonFinalizationFailed,
			wantMode:   FinalizePublish,
		},
		{
			name: "finalization timeout",
			configure: func(executor *fakeExecutor) {
				executor.finalizeFn = func(ctx context.Context, _ FinalizeMode) error {
					<-ctx.Done()
					return ctx.Err()
				}
			},
			wantReason: FailureReasonFinalizationTimeout,
			wantMode:   FinalizePublish,
		},
		{
			name: "stop failure",
			configure: func(executor *fakeExecutor) {
				stopCalled := make(chan struct{})
				executor.waitFn = func() error {
					<-stopCalled
					return nil
				}
				executor.stopFn = func(context.Context) error {
					close(stopCalled)
					return errors.New("stop failed")
				}
			},
			stop:       true,
			wantReason: FailureReasonExecutionStopFailed,
			wantMode:   FinalizeDiscard,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := testConfig()
			config.Lifecycle.FinalizationTimeout = 10 * time.Millisecond
			manager := newTestManager(t, config)
			executor := &fakeExecutor{}
			test.configure(executor)
			_, _, err := manager.Start(StartRequest{
				RequestID: "request",
				Kind:      KindProfiling,
				Executor:  executor,
			})
			if err != nil {
				t.Fatalf("Start() error = %v", err)
			}
			if test.stop {
				waitForStatus(t, manager, KindProfiling, "request", StatusRunning)
				if _, initiated, stopErr := manager.StopByID("request"); stopErr != nil || !initiated {
					t.Fatalf("Stop() = (_, %t, %v), want initiated", initiated, stopErr)
				}
			}
			operation := waitForStatus(t, manager, KindProfiling, "request", StatusTerminal)
			if operation.Terminal == nil || operation.Terminal.Reason != test.wantReason {
				t.Fatalf("terminal = %+v, want reason %s", operation.Terminal, test.wantReason)
			}
			modes := executor.modes()
			if len(modes) != 1 || modes[0] != test.wantMode {
				t.Errorf("finalize modes = %v, want [%d]", modes, test.wantMode)
			}
		})
	}
}

func TestManagerShutdownStopsOperationsOnce(t *testing.T) {
	manager, err := NewManager(testConfig())
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	executors := make([]*fakeExecutor, 2)
	for index := range executors {
		stopCalled := make(chan struct{})
		var stopOnce sync.Once
		executors[index] = &fakeExecutor{
			waitFn: func() error {
				<-stopCalled
				return ErrStopped
			},
			stopFn: func(context.Context) error {
				stopOnce.Do(func() { close(stopCalled) })
				return nil
			},
		}
		requestID := fmt.Sprintf("request-%d", index)
		_, _, startErr := manager.Start(StartRequest{
			RequestID: requestID,
			Kind:      KindProfiling,
			Executor:  executors[index],
		})
		if startErr != nil {
			t.Fatalf("Start(%q) error = %v", requestID, startErr)
		}
		waitForStatus(t, manager, KindProfiling, requestID, StatusRunning)
	}

	manager.BeginShutdown()
	_, _, err = manager.Start(StartRequest{
		RequestID: "rejected",
		Kind:      KindProfiling,
		Executor:  &fakeExecutor{},
	})
	if !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("Start() during shutdown error = %v, want ErrShuttingDown", err)
	}

	const callers = 3
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), testWaitTimeout)
			defer cancel()
			if shutdownErr := manager.Shutdown(ctx); shutdownErr != nil {
				t.Errorf("Shutdown() error = %v", shutdownErr)
			}
		}()
	}
	wg.Wait()

	for index, executor := range executors {
		_, _, stopCalls, finalizeCalls := executor.counts()
		if stopCalls != 1 || finalizeCalls != 1 {
			t.Errorf("executor %d calls = (stop %d, finalize %d), want one each", index, stopCalls, finalizeCalls)
		}
	}
}

func TestManagerShutdownCallerTimeoutDoesNotCancelShutdown(t *testing.T) {
	manager, err := NewManager(testConfig())
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	waitRelease := make(chan struct{})
	stopCalled := make(chan struct{})
	executor := &fakeExecutor{
		waitFn: func() error {
			<-waitRelease
			return ErrStopped
		},
		stopFn: func(context.Context) error {
			close(stopCalled)
			return nil
		},
	}
	_, _, err = manager.Start(StartRequest{
		RequestID: "request",
		Kind:      KindProfiling,
		Executor:  executor,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForStatus(t, manager, KindProfiling, "request", StatusRunning)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if err := manager.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Shutdown() error = %v, want context deadline", err)
	}
	waitClosed(t, stopCalled, "executor Stop")
	close(waitRelease)

	ctx, cancel = context.WithTimeout(context.Background(), testWaitTimeout)
	defer cancel()
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatalf("second Shutdown() error = %v", err)
	}
	operation, err := manager.GetByID("request")
	if err != nil || operation.Status != StatusTerminal || operation.Terminal == nil || operation.Terminal.Outcome != OutcomeStopped {
		t.Fatalf("operation after Shutdown() = (%+v, %v), want stopped terminal", operation, err)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

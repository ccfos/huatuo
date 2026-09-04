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
	"strings"
	"sync"
	"testing"
	"time"
)

const testWaitTimeout = 2 * time.Second

type fakeExecutor struct {
	startFn    func(context.Context) error
	waitFn     func() error
	stopFn     func(context.Context) error
	finalizeFn func(context.Context, FinalizeMode) error

	mu            sync.Mutex
	calls         []string
	startCalls    int
	waitCalls     int
	stopCalls     int
	finalizeCalls int
	finalizeModes []FinalizeMode
}

func (e *fakeExecutor) Start(ctx context.Context) error {
	e.mu.Lock()
	e.startCalls++
	e.calls = append(e.calls, "start")
	fn := e.startFn
	e.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

func (e *fakeExecutor) Wait() error {
	e.mu.Lock()
	e.waitCalls++
	e.calls = append(e.calls, "wait")
	fn := e.waitFn
	e.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn()
}

func (e *fakeExecutor) Stop(ctx context.Context) error {
	e.mu.Lock()
	e.stopCalls++
	e.calls = append(e.calls, "stop")
	fn := e.stopFn
	e.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

func (e *fakeExecutor) Finalize(ctx context.Context, mode FinalizeMode) error {
	e.mu.Lock()
	e.finalizeCalls++
	e.finalizeModes = append(e.finalizeModes, mode)
	e.calls = append(e.calls, fmt.Sprintf("finalize:%d", mode))
	fn := e.finalizeFn
	e.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, mode)
}

func (e *fakeExecutor) counts() (start, wait, stop, finalize int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.startCalls, e.waitCalls, e.stopCalls, e.finalizeCalls
}

func (e *fakeExecutor) callSequence() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.calls...)
}

func (e *fakeExecutor) modes() []FinalizeMode {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]FinalizeMode(nil), e.finalizeModes...)
}

func TestNewManagerValidatesConfig(t *testing.T) {
	valid := testConfig()
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name: "max concurrent",
			mutate: func(config *Config) {
				config.MaxConcurrent = 0
			},
			wantErr: "max concurrent",
		},
		{
			name: "launch timeout",
			mutate: func(config *Config) {
				config.Lifecycle.LaunchTimeout = 0
			},
			wantErr: "launch timeout",
		},
		{
			name: "stop grace period",
			mutate: func(config *Config) {
				config.Lifecycle.StopGracePeriod = 0
			},
			wantErr: "stop grace period",
		},
		{
			name: "finalization timeout",
			mutate: func(config *Config) {
				config.Lifecycle.FinalizationTimeout = 0
			},
			wantErr: "finalization timeout",
		},
		{
			name: "terminal retention period",
			mutate: func(config *Config) {
				config.Lifecycle.TerminalRetentionPeriod = 0
			},
			wantErr: "terminal retention period",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			_, err := NewManager(config)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("NewManager() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestManagerValidatesRequests(t *testing.T) {
	manager := newTestManager(t, testConfig())
	executor := &fakeExecutor{}

	tests := []struct {
		name      string
		ctx       context.Context
		request   StartRequest
		wantError error
	}{
		{
			name:      "nil context",
			request:   StartRequest{RequestID: "request", Kind: KindProfiling, Executor: executor},
			wantError: ErrInvalidRequest,
		},
		{
			name:      "missing request ID",
			ctx:       context.Background(),
			request:   StartRequest{Kind: KindProfiling, Executor: executor},
			wantError: ErrInvalidRequest,
		},
		{
			name:      "invalid kind",
			ctx:       context.Background(),
			request:   StartRequest{RequestID: "request", Kind: "unknown", Executor: executor},
			wantError: ErrInvalidRequest,
		},
		{
			name:      "missing executor",
			ctx:       context.Background(),
			request:   StartRequest{RequestID: "request", Kind: KindProfiling},
			wantError: ErrInvalidRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, created, err := manager.Start(test.ctx, test.request)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("Start() error = %v, want %v", err, test.wantError)
			}
			if created {
				t.Fatal("Start() created = true, want false")
			}
		})
	}

	if _, err := manager.GetByID(""); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("GetByID() error = %v, want ErrInvalidRequest", err)
	}
	if _, _, err := manager.StopByID(""); !errors.Is(err, ErrInvalidRequest) {
		t.Errorf("Stop() error = %v, want ErrInvalidRequest", err)
	}
}

func TestManagerGetAndStopByID(t *testing.T) {
	manager := newTestManager(t, testConfig())
	if _, created, err := manager.Start(context.Background(), StartRequest{
		RequestID: "request",
		Kind:      KindTracing,
		Executor:  &fakeExecutor{},
	}); err != nil || !created {
		t.Fatalf("Start() = (created=%t, err=%v), want created operation", created, err)
	}

	got, err := manager.GetByID("request")
	if err != nil || got.Kind != KindTracing {
		t.Fatalf("GetByID() = (%+v, %v), want tracing operation", got, err)
	}
	if _, initiated, err := manager.StopByID("request"); err != nil || !initiated {
		t.Fatalf("StopByID() = (initiated=%t, err=%v), want initiated stop", initiated, err)
	}
	waitForStatus(t, manager, KindTracing, "request", StatusStopped)
}

func TestManagerAdmissionAndIdempotency(t *testing.T) {
	config := testConfig()
	config.MaxConcurrent = 1
	manager := newTestManager(t, config)
	startEntered := make(chan struct{})
	executor := &fakeExecutor{
		startFn: func(ctx context.Context) error {
			close(startEntered)
			<-ctx.Done()
			return ctx.Err()
		},
	}

	createdOperation, created, err := manager.Start(context.Background(), StartRequest{
		RequestID: "request-1",
		Kind:      KindProfiling,
		Executor:  executor,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if !created || createdOperation.Status != StatusPending {
		t.Fatalf("Start() = (%+v, %t), want pending and created", createdOperation, created)
	}
	waitClosed(t, startEntered, "executor Start")

	duplicateExecutor := &fakeExecutor{}
	duplicate, duplicateCreated, err := manager.Start(context.Background(), StartRequest{
		RequestID: "request-1",
		Kind:      KindProfiling,
		Executor:  duplicateExecutor,
	})
	if err != nil {
		t.Fatalf("duplicate Start() error = %v", err)
	}
	if duplicateCreated || duplicate.RequestID != "request-1" {
		t.Fatalf("duplicate Start() = (%+v, %t), want existing operation", duplicate, duplicateCreated)
	}
	startCalls, _, _, _ := duplicateExecutor.counts()
	if startCalls != 0 {
		t.Fatalf("duplicate executor Start calls = %d, want 0", startCalls)
	}

	_, _, err = manager.Start(context.Background(), StartRequest{
		RequestID: "request-1",
		Kind:      KindTracing,
		Executor:  &fakeExecutor{},
	})
	if !errors.Is(err, ErrRequestIDConflict) {
		t.Fatalf("conflicting Start() error = %v, want ErrRequestIDConflict", err)
	}
	_, _, err = manager.Start(context.Background(), StartRequest{
		RequestID: "request-2",
		Kind:      KindProfiling,
		Executor:  &fakeExecutor{},
	})
	if !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("capacity Start() error = %v, want ErrLimitExceeded", err)
	}

	manager.BeginShutdown()
	if _, duplicateCreated, err = manager.Start(context.Background(), StartRequest{
		RequestID: "request-1",
		Kind:      KindProfiling,
		Executor:  &fakeExecutor{},
	}); err != nil || duplicateCreated {
		t.Fatalf("shutdown duplicate Start() = (_, %t, %v), want existing operation", duplicateCreated, err)
	}
	_, _, err = manager.Start(context.Background(), StartRequest{
		RequestID: "request-3",
		Kind:      KindProfiling,
		Executor:  &fakeExecutor{},
	})
	if !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("shutdown Start() error = %v, want ErrShuttingDown", err)
	}
}

func TestManagerReturnsDetachedSnapshots(t *testing.T) {
	manager := newTestManager(t, testConfig())
	executor := &fakeExecutor{startFn: func(context.Context) error {
		return errors.New("start failed with private detail")
	}}
	_, _, err := manager.Start(context.Background(), StartRequest{
		RequestID: "request",
		Kind:      KindProfiling,
		Executor:  executor,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	operation := waitForStatus(t, manager, KindProfiling, "request", StatusFailed)
	operation.Failure.Message = "mutated"
	*operation.FinishedAt = time.Time{}

	second, err := manager.GetByID("request")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if second.Failure.Message != messageExecutionStartFailed {
		t.Errorf("failure message = %q, want %q", second.Failure.Message, messageExecutionStartFailed)
	}
	if second.FinishedAt.IsZero() {
		t.Error("finished time was mutated through returned snapshot")
	}
}

func TestManagerDoesNotInheritRequestContext(t *testing.T) {
	type contextKey struct{}

	manager := newTestManager(t, testConfig())
	startRelease := make(chan struct{})
	contextChecked := make(chan struct{})
	executor := &fakeExecutor{startFn: func(ctx context.Context) error {
		if value := ctx.Value(contextKey{}); value != nil {
			t.Errorf("executor context value = %v, want nil", value)
		}
		if err := ctx.Err(); err != nil {
			t.Errorf("executor context error = %v, want nil", err)
		}
		close(contextChecked)
		<-startRelease
		return nil
	}}
	requestCtx, cancelRequest := context.WithCancel(
		context.WithValue(context.Background(), contextKey{}, "request value"),
	)
	_, _, err := manager.Start(requestCtx, StartRequest{
		RequestID: "request",
		Kind:      KindProfiling,
		Executor:  executor,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	cancelRequest()
	waitClosed(t, contextChecked, "executor context check")
	close(startRelease)
	waitForStatus(t, manager, KindProfiling, "request", StatusCompleted)
}

func TestManagerReusesExpiredRequestID(t *testing.T) {
	config := testConfig()
	config.Lifecycle.TerminalRetentionPeriod = 20 * time.Millisecond
	manager := newTestManager(t, config)
	_, _, err := manager.Start(context.Background(), StartRequest{
		RequestID: "request",
		Kind:      KindProfiling,
		Executor:  &fakeExecutor{},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForStatus(t, manager, KindProfiling, "request", StatusCompleted)
	time.Sleep(25 * time.Millisecond)

	startBlock := make(chan struct{})
	replacement := &fakeExecutor{startFn: func(ctx context.Context) error {
		select {
		case <-startBlock:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	operation, created, err := manager.Start(context.Background(), StartRequest{
		RequestID: "request",
		Kind:      KindTracing,
		Executor:  replacement,
	})
	if err != nil {
		t.Fatalf("replacement Start() error = %v", err)
	}
	if !created || operation.Kind != KindTracing || operation.Status != StatusPending {
		t.Fatalf("replacement Start() = (%+v, %t), want new tracing operation", operation, created)
	}
	close(startBlock)
	waitForStatus(t, manager, KindTracing, "request", StatusCompleted)
}

func TestManagerTerminalRetention(t *testing.T) {
	manager := newTestManager(t, testConfig())
	_, _, err := manager.Start(context.Background(), StartRequest{
		RequestID: "terminal",
		Kind:      KindProfiling,
		Executor:  &fakeExecutor{},
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitForStatus(t, manager, KindProfiling, "terminal", StatusCompleted)

	manager.mu.RLock()
	expiresAt := manager.operations["terminal"].expiresAt
	manager.mu.RUnlock()
	if _, err := manager.GetByID("terminal"); err != nil {
		t.Fatalf("retained Get() error = %v", err)
	}
	manager.mu.RLock()
	retainedExpiry := manager.operations["terminal"].expiresAt
	manager.mu.RUnlock()
	if !retainedExpiry.Equal(expiresAt) {
		t.Fatalf("Get() extended expiry from %v to %v", expiresAt, retainedExpiry)
	}
	manager.now = func() time.Time { return expiresAt }
	if _, err := manager.GetByID("terminal"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired Get() error = %v, want ErrNotFound", err)
	}
	manager.mu.RLock()
	_, retained := manager.operations["terminal"]
	manager.mu.RUnlock()
	if !retained {
		t.Fatal("Get() physically deleted terminal record")
	}

	manager.cleanupExpired(expiresAt)
	manager.mu.RLock()
	_, retained = manager.operations["terminal"]
	manager.mu.RUnlock()
	if retained {
		t.Fatal("cleanupExpired() retained expired terminal record")
	}
}

func TestManagerCleanupKeepsActiveOperation(t *testing.T) {
	manager := newTestManager(t, testConfig())
	startEntered := make(chan struct{})
	executor := &fakeExecutor{startFn: func(ctx context.Context) error {
		close(startEntered)
		<-ctx.Done()
		return ctx.Err()
	}}
	_, _, err := manager.Start(context.Background(), StartRequest{
		RequestID: "active",
		Kind:      KindProfiling,
		Executor:  executor,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	waitClosed(t, startEntered, "executor Start")
	manager.cleanupExpired(time.Now().Add(24 * time.Hour))
	if _, err := manager.GetByID("active"); err != nil {
		t.Fatalf("Get() after cleanup error = %v", err)
	}
}

func TestManagerStartAndBeginShutdownAreSerialized(t *testing.T) {
	const attempts = 32
	config := testConfig()
	config.MaxConcurrent = attempts
	manager := newTestManager(t, config)

	startGate := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(attempts)
	for index := range attempts {
		go func() {
			defer wg.Done()
			<-startGate
			requestID := fmt.Sprintf("request-%d", index)
			executor := &fakeExecutor{startFn: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			}}
			_, _, err := manager.Start(context.Background(), StartRequest{
				RequestID: requestID,
				Kind:      KindProfiling,
				Executor:  executor,
			})
			if err != nil && !errors.Is(err, ErrShuttingDown) {
				t.Errorf("Start(%q) error = %v", requestID, err)
			}
		}()
	}
	close(startGate)
	manager.BeginShutdown()
	wg.Wait()

	manager.mu.RLock()
	activeCount := manager.activeCount
	operationCount := len(manager.operations)
	isAccepting := manager.isAccepting
	manager.mu.RUnlock()
	if isAccepting {
		t.Error("manager still accepts operations")
	}
	if activeCount != operationCount {
		t.Errorf("active count = %d, operation count = %d", activeCount, operationCount)
	}
}

func testConfig() Config {
	return Config{
		MaxConcurrent: 4,
		Lifecycle: LifecyclePolicy{
			LaunchTimeout:           100 * time.Millisecond,
			StopGracePeriod:         100 * time.Millisecond,
			FinalizationTimeout:     100 * time.Millisecond,
			TerminalRetentionPeriod: time.Minute,
		},
	}
}

func newTestManager(t *testing.T, config Config) *Manager {
	t.Helper()
	manager, err := NewManager(config)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), testWaitTimeout)
		defer cancel()
		if err := manager.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() cleanup error = %v", err)
		}
	})
	return manager
}

func waitForStatus(
	t *testing.T,
	manager *Manager,
	kind Kind,
	requestID string,
	want Status,
) *Operation {
	t.Helper()
	_ = kind
	deadline := time.Now().Add(testWaitTimeout)
	for time.Now().Before(deadline) {
		operation, err := manager.GetByID(requestID)
		if err == nil && operation.Status == want {
			return operation
		}
		time.Sleep(time.Millisecond)
	}
	operation, err := manager.GetByID(requestID)
	t.Fatalf("operation after wait = (%+v, %v), want status %s", operation, err, want)
	return nil
}

func waitForFinalizing(t *testing.T, manager *Manager, requestID string) {
	t.Helper()
	deadline := time.Now().Add(testWaitTimeout)
	for time.Now().Before(deadline) {
		manager.mu.RLock()
		managed := manager.operations[requestID]
		finalizing := managed != nil && managed.isFinalizing
		manager.mu.RUnlock()
		if finalizing {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("operation %q did not enter finalizing", requestID)
}

func waitClosed(t *testing.T, channel <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(testWaitTimeout):
		t.Fatalf("timed out waiting for %s", description)
	}
}

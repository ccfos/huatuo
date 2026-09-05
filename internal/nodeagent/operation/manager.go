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
	"time"
)

const terminalCleanupInterval = 30 * time.Second

type executionRuntime struct {
	executor      Executor
	launchCancel  context.CancelFunc
	stopResultCh  chan error
	isStopStarted bool
}

type managedOperation struct {
	state        Operation
	execution    *executionRuntime
	isFinalizing bool
	expiresAt    time.Time
}

type stopAction struct {
	executor Executor
	resultCh chan<- error
}

// Manager owns all in-memory Node operation state and lifecycle goroutines.
type Manager struct {
	mu sync.RWMutex

	operations  map[string]*managedOperation
	activeCount int
	isAccepting bool

	maxConcurrent int
	lifecycle     LifecyclePolicy
	cleanupStopCh chan struct{}
	wg            sync.WaitGroup

	shutdownOnce sync.Once
	shutdownDone chan struct{}
	now          func() time.Time
}

// NewManager validates config and starts the terminal-record cleanup routine.
func NewManager(config Config) (*Manager, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}

	manager := &Manager{
		operations:    make(map[string]*managedOperation),
		isAccepting:   true,
		maxConcurrent: config.MaxConcurrent,
		lifecycle:     config.Lifecycle,
		cleanupStopCh: make(chan struct{}),
		shutdownDone:  make(chan struct{}),
		now:           time.Now,
	}
	manager.wg.Add(1)
	go manager.cleanupLoop()
	return manager, nil
}

// Start synchronously registers an operation and starts its lifecycle in the
// background.
func (m *Manager) Start(request StartRequest) (operation *Operation, created bool, err error) {
	if err := validateStartRequest(request); err != nil {
		return nil, false, err
	}

	m.mu.Lock()
	now := m.now()
	if existing, ok := m.operations[request.RequestID]; ok {
		if m.isExpiredLocked(existing, now) {
			delete(m.operations, request.RequestID)
		} else if existing.state.Kind != request.Kind {
			m.mu.Unlock()
			return nil, false, ErrRequestIDConflict
		} else {
			snapshot := cloneOperation(&existing.state)
			m.mu.Unlock()
			return snapshot, false, nil
		}
	}
	if !m.isAccepting {
		m.mu.Unlock()
		return nil, false, ErrShuttingDown
	}
	if m.activeCount >= m.maxConcurrent {
		m.mu.Unlock()
		return nil, false, ErrLimitExceeded
	}

	runtime := &executionRuntime{
		executor:     request.Executor,
		stopResultCh: make(chan error, 1),
	}
	managed := &managedOperation{
		state: Operation{
			RequestID: request.RequestID,
			Kind:      request.Kind,
			Status:    StatusPending,
			CreatedAt: now,
		},
		execution: runtime,
	}
	m.operations[request.RequestID] = managed
	m.activeCount++
	m.wg.Add(1)
	snapshot := cloneOperation(&managed.state)
	m.mu.Unlock()

	go m.runOperation(managed)
	return snapshot, true, nil
}

// GetByID returns a detached snapshot by request ID.
func (m *Manager) GetByID(requestID string) (*Operation, error) {
	m.mu.RLock()
	now := m.now()
	managed, ok := m.operations[requestID]
	if !ok || m.isExpiredLocked(managed, now) {
		m.mu.RUnlock()
		return nil, ErrNotFound
	}
	snapshot := cloneOperation(&managed.state)
	m.mu.RUnlock()
	return snapshot, nil
}

// StopByID records one asynchronous stop intent. Repeated requests are
// idempotent.
func (m *Manager) StopByID(
	requestID string,
) (operation *Operation, initiated bool, err error) {
	m.mu.Lock()
	now := m.now()
	managed, ok := m.operations[requestID]
	if !ok {
		m.mu.Unlock()
		return nil, false, ErrNotFound
	}
	if m.isExpiredLocked(managed, now) {
		delete(m.operations, requestID)
		m.mu.Unlock()
		return nil, false, ErrNotFound
	}

	initiated, cancelLaunch, action := m.requestStopLocked(managed)
	snapshot := cloneOperation(&managed.state)
	m.mu.Unlock()

	if cancelLaunch != nil {
		cancelLaunch()
	}
	m.startStop(action)
	return snapshot, initiated, nil
}

// BeginShutdown atomically rejects all subsequent new operations.
func (m *Manager) BeginShutdown() {
	m.mu.Lock()
	m.isAccepting = false
	m.mu.Unlock()
}

// Shutdown shares one underlying stop-and-wait process across all callers.
func (m *Manager) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("shut down operation manager: context is required")
	}

	m.BeginShutdown()
	m.shutdownOnce.Do(m.startShutdown)
	if err := ctx.Err(); err != nil {
		return err
	}

	select {
	case <-m.shutdownDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func validateConfig(config Config) error {
	if config.MaxConcurrent <= 0 {
		return errors.New("create operation manager: max concurrent must be greater than zero")
	}
	if config.Lifecycle.LaunchTimeout <= 0 {
		return errors.New("create operation manager: launch timeout must be greater than zero")
	}
	if config.Lifecycle.StopGracePeriod <= 0 {
		return errors.New("create operation manager: stop grace period must be greater than zero")
	}
	if config.Lifecycle.FinalizationTimeout <= 0 {
		return errors.New("create operation manager: finalization timeout must be greater than zero")
	}
	if config.Lifecycle.TerminalRetentionPeriod <= 0 {
		return errors.New("create operation manager: terminal retention period must be greater than zero")
	}
	return nil
}

func validateStartRequest(request StartRequest) error {
	if request.RequestID == "" {
		return fmt.Errorf("%w: request ID is required", ErrInvalidRequest)
	}
	if !isValidKind(request.Kind) {
		return fmt.Errorf("%w: unsupported kind %q", ErrInvalidRequest, request.Kind)
	}
	if request.Executor == nil {
		return fmt.Errorf("%w: executor is required", ErrInvalidRequest)
	}
	return nil
}

func (m *Manager) requestStopLocked(
	managed *managedOperation,
) (initiated bool, cancelLaunch context.CancelFunc, action *stopAction) {
	if isTerminal(managed.state.Status) || managed.isFinalizing || managed.state.Status == StatusStopping {
		return false, nil, nil
	}

	previousStatus := managed.state.Status
	managed.state.Status = StatusStopping
	switch previousStatus {
	case StatusPending:
		if managed.execution == nil {
			return true, nil, nil
		}
		return true, managed.execution.launchCancel, nil
	case StatusRunning:
		return true, nil, m.prepareStopLocked(managed)
	default:
		return true, nil, nil
	}
}

func (m *Manager) prepareStopLocked(managed *managedOperation) *stopAction {
	runtime := managed.execution
	if runtime == nil || runtime.isStopStarted {
		return nil
	}
	runtime.isStopStarted = true
	m.wg.Add(1)
	return &stopAction{
		executor: runtime.executor,
		resultCh: runtime.stopResultCh,
	}
}

func (m *Manager) startStop(action *stopAction) {
	if action == nil {
		return
	}
	go func() {
		defer m.wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), m.lifecycle.StopGracePeriod)
		defer cancel()
		action.resultCh <- action.executor.Stop(ctx)
	}()
}

func (m *Manager) startShutdown() {
	var (
		cancels []context.CancelFunc
		actions []*stopAction
	)

	m.mu.Lock()
	for _, managed := range m.operations {
		if isTerminal(managed.state.Status) {
			continue
		}
		_, cancelLaunch, action := m.requestStopLocked(managed)
		if cancelLaunch != nil {
			cancels = append(cancels, cancelLaunch)
		}
		if action != nil {
			actions = append(actions, action)
		}
	}
	close(m.cleanupStopCh)
	m.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	for _, action := range actions {
		m.startStop(action)
	}
	go func() {
		m.wg.Wait()
		close(m.shutdownDone)
	}()
}

func (m *Manager) cleanupLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(terminalCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case now := <-ticker.C:
			m.cleanupExpired(now)
		case <-m.cleanupStopCh:
			return
		}
	}
}

func (m *Manager) cleanupExpired(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for requestID, managed := range m.operations {
		if m.isExpiredLocked(managed, now) {
			delete(m.operations, requestID)
		}
	}
}

func (m *Manager) isExpiredLocked(managed *managedOperation, now time.Time) bool {
	return isTerminal(managed.state.Status) &&
		managed.execution == nil &&
		!managed.expiresAt.IsZero() &&
		!now.Before(managed.expiresAt)
}

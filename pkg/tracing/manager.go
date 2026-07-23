// Copyright 2025, 2026 The HuaTuo Authors
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
	"fmt"
	"sync"
	"time"
)

// Manager owns the registered tracer runners. It is safe for simultaneous use
// by multiple goroutines.
type Manager struct {
	mu       sync.RWMutex
	runners  map[string]*eventRunner
	isClosed bool
}

// NewManager initializes all registered tracers that are not blacklisted.
func NewManager(blacklist []string) (*Manager, error) {
	registrations, err := NewRegister(blacklist)
	if err != nil {
		return nil, err
	}

	runners := make(map[string]*eventRunner, len(registrations))
	for name, registration := range registrations {
		if registration.Flag&FlagTracing == 0 {
			continue
		}

		starter, ok := registration.TracingData.(starter)
		if !ok {
			return nil, fmt.Errorf(
				"%w: %q does not implement Start(context.Context)",
				ErrInvalidTracer,
				name,
			)
		}
		if registration.Interval <= 0 {
			return nil, fmt.Errorf(
				"%w: %q restart interval must be positive",
				ErrInvalidTracer,
				name,
			)
		}

		runners[name] = newEventRunner(
			name,
			starter,
			time.Duration(registration.Interval)*time.Second,
			registration.Flag,
		)
	}

	return &Manager{runners: runners}, nil
}

// Start starts every registered tracer.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.isClosed {
		return ErrManagerClosed
	}

	var errs []error
	for _, runner := range m.runners {
		if err := runner.start(ctx); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// StartByName starts a registered tracer.
func (m *Manager) StartByName(ctx context.Context, name string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.isClosed {
		return ErrManagerClosed
	}

	runner, ok := m.runners[name]
	if !ok {
		return newTracerStateError(ErrTracerNotFound, name)
	}

	return runner.start(ctx)
}

// Close permanently rejects subsequent starts, cancels all tracers, and waits
// for their goroutines until ctx is done.
//
// A closed Manager cannot be restarted because application shutdown may
// release stores and BPF resources immediately afterward. Use StopByName for a
// restartable stop, or create a new Manager after Close. If ctx ends first,
// Close returns its error while the tracer goroutines continue shutting down.
func (m *Manager) Close(ctx context.Context) error {
	m.mu.Lock()
	m.isClosed = true

	type pendingStop struct {
		name string
		done <-chan struct{}
	}

	pending := make([]pendingStop, 0, len(m.runners))
	var errs []error
	for name, runner := range m.runners {
		done, err := runner.cancelRun()
		if err != nil && !errors.Is(err, ErrTracerNotRunning) {
			errs = append(errs, err)
			continue
		}
		if done != nil {
			pending = append(pending, pendingStop{name: name, done: done})
		}
	}
	m.mu.Unlock()

	for _, runner := range pending {
		if err := waitForRunner(ctx, runner.name, runner.done); err != nil {
			errs = append(errs, err)
		}
	}

	return errors.Join(errs...)
}

// StopByName stops a registered tracer and waits for its goroutine to exit.
func (m *Manager) StopByName(ctx context.Context, name string) error {
	m.mu.RLock()
	runner, ok := m.runners[name]
	m.mu.RUnlock()
	if !ok {
		return newTracerStateError(ErrTracerNotFound, name)
	}

	return runner.stop(ctx)
}

// Snapshots returns lifecycle snapshots for all registered tracers.
func (m *Manager) Snapshots() map[string]LifecycleSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	snapshots := make(map[string]LifecycleSnapshot, len(m.runners))
	for name, runner := range m.runners {
		snapshots[name] = runner.snapshot()
	}

	return snapshots
}

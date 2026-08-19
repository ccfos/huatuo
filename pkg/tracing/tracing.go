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

// Package tracing schedules tracers and manages their lifecycle.
package tracing

import (
	"context"
	"errors"
	"sync"
	"time"

	"huatuo-bamai/internal/log"
	"huatuo-bamai/pkg/types"
)

type starter interface {
	Start(ctx context.Context) error
}

type eventRunner struct {
	starter         starter
	name            string
	restartInterval time.Duration
	roles           uint32

	mu          sync.RWMutex
	cancel      context.CancelFunc
	active      *runCompletion
	completions []*runCompletion
	runCount    int
}

type runCompletion struct {
	done        chan struct{}
	terminalErr error
}

func newEventRunner(
	name string,
	starter starter,
	restartInterval time.Duration,
	roles uint32,
) *eventRunner {
	return &eventRunner{
		starter:         starter,
		name:            name,
		restartInterval: restartInterval,
		roles:           roles,
	}
}

func (r *eventRunner) start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return newTracerContextError("start", r.name, err)
	}

	r.mu.Lock()
	if r.active != nil {
		r.mu.Unlock()
		return newTracerStateError(ErrTracerAlreadyRunning, r.name)
	}
	r.discardSuccessfulCompletionsLocked()
	if len(r.completions) != 0 {
		r.mu.Unlock()
		return newTracerRunErrorPending(r.name)
	}

	runCtx, cancel := context.WithCancel(ctx)
	completion := &runCompletion{done: make(chan struct{})}
	r.cancel = cancel
	r.active = completion
	r.completions = append(r.completions, completion)
	r.mu.Unlock()

	go r.run(runCtx, completion)

	return nil
}

func (r *eventRunner) discardSuccessfulCompletionsLocked() {
	live := r.completions[:0]
	for _, completion := range r.completions {
		if completion.terminalErr != nil {
			live = append(live, completion)
		}
	}
	clear(r.completions[len(live):])
	r.completions = live
}

func (r *eventRunner) run(ctx context.Context, completion *runCompletion) {
	log.WithField("tracer", r.name).Info("tracer started")
	var terminalErr error
	defer func() {
		r.finish(completion, terminalErr)
	}()

	for {
		err := r.starter.Start(ctx)
		r.incrementRunCount()

		if ctx.Err() != nil {
			terminalErr = unexpectedStopError(err)
			return
		}
		if errors.Is(err, types.ErrNotSupported) {
			terminalErr = unexpectedStopError(err)
			return
		}

		if err != nil &&
			!errors.Is(err, types.ErrExitByCancelCtx) &&
			!errors.Is(err, types.ErrDisconnectedHuatuo) {
			log.WithError(err).
				WithField("tracer", r.name).
				Error("tracer failed")
		}

		timer := time.NewTimer(r.restartInterval)
		select {
		case <-ctx.Done():
			timer.Stop()

			return
		case <-timer.C:
		}
	}
}

func (r *eventRunner) finish(completion *runCompletion, terminalErr error) {
	r.mu.Lock()
	completion.terminalErr = terminalErr
	if r.active == completion {
		r.cancel = nil
		r.active = nil
	}
	close(completion.done)
	r.mu.Unlock()

	log.WithField("tracer", r.name).Info("tracer stopped")
}

func unexpectedStopError(err error) error {
	if err == nil || containsOnlyStopErrors(err) {
		return nil
	}
	return err
}

func containsOnlyStopErrors(err error) bool {
	if err == nil {
		return true
	}

	var multiple interface{ Unwrap() []error }
	if errors.As(err, &multiple) {
		errs := multiple.Unwrap()
		if len(errs) == 0 {
			return isStopError(err)
		}
		for _, unwrapped := range errs {
			if !containsOnlyStopErrors(unwrapped) {
				return false
			}
		}
		return true
	}

	var single interface{ Unwrap() error }
	if errors.As(err, &single) {
		unwrapped := single.Unwrap()
		if unwrapped != nil {
			return containsOnlyStopErrors(unwrapped)
		}
	}

	return isStopError(err)
}

func isStopError(err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, types.ErrExitByCancelCtx) ||
		errors.Is(err, types.ErrDisconnectedHuatuo) ||
		errors.Is(err, types.ErrNotSupported)
}

func (r *eventRunner) incrementRunCount() {
	r.mu.Lock()
	r.runCount++
	r.mu.Unlock()
}

func (r *eventRunner) stop(ctx context.Context) error {
	cancel, completions := r.prepareStop()
	if len(completions) == 0 {
		return newTracerStateError(ErrTracerNotRunning, r.name)
	}

	return r.stopCompletions(ctx, cancel, completions)
}

func (r *eventRunner) stopCompletions(
	ctx context.Context,
	cancel context.CancelFunc,
	completions []*runCompletion,
) error {
	if cancel != nil {
		cancel()
	}

	return r.waitForCompletions(ctx, completions)
}

func (r *eventRunner) prepareStop() (context.CancelFunc, []*runCompletion) {
	r.mu.RLock()
	cancel := r.cancel
	completions := append([]*runCompletion(nil), r.completions...)
	r.mu.RUnlock()

	return cancel, completions
}

func (r *eventRunner) waitForCompletions(
	ctx context.Context,
	completions []*runCompletion,
) error {
	var errs []error
	for _, completion := range completions {
		if err := waitForRunner(ctx, r.name, completion.done); err != nil {
			errs = append(errs, err)
			continue
		}

		terminalErr, ok := r.consumeCompletion(completion)
		if ok && terminalErr != nil {
			errs = append(errs, newTracerRunError(r.name, terminalErr))
		}
	}

	return errors.Join(errs...)
}

func (r *eventRunner) consumeCompletion(completion *runCompletion) (error, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i, pending := range r.completions {
		if pending != completion {
			continue
		}

		terminalErr := pending.terminalErr
		copy(r.completions[i:], r.completions[i+1:])
		r.completions[len(r.completions)-1] = nil
		r.completions = r.completions[:len(r.completions)-1]
		return terminalErr, true
	}

	return nil, false
}

func waitForRunner(ctx context.Context, name string, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		select {
		case <-done:
			return nil
		default:
			return newTracerContextError("stop", name, ctx.Err())
		}
	}
}

// LifecycleSnapshot contains a tracer's current lifecycle state.
type LifecycleSnapshot struct {
	Name            string `json:"name"`
	IsRunning       bool   `json:"running"`
	RunCount        int    `json:"hit"`
	RestartInterval int    `json:"restart_interval"`
	Roles           uint32 `json:"flag"`
}

func (r *eventRunner) snapshot() LifecycleSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return LifecycleSnapshot{
		Name:            r.name,
		IsRunning:       r.active != nil,
		RunCount:        r.runCount,
		RestartInterval: int(r.restartInterval / time.Second),
		Roles:           r.roles,
	}
}

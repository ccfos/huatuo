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

	mu       sync.RWMutex
	cancel   context.CancelFunc
	done     <-chan struct{}
	runCount int
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
	if r.done != nil {
		r.mu.Unlock()
		return newTracerStateError(ErrTracerAlreadyRunning, r.name)
	}

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.cancel = cancel
	r.done = done
	r.mu.Unlock()

	go r.run(runCtx, done)

	return nil
}

func (r *eventRunner) run(ctx context.Context, done chan<- struct{}) {
	log.WithField("tracer", r.name).Info("tracer started")
	defer r.finish(done)

	for {
		err := r.starter.Start(ctx)
		r.incrementRunCount()

		if ctx.Err() != nil || errors.Is(err, types.ErrNotSupported) {
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

func (r *eventRunner) finish(done chan<- struct{}) {
	r.mu.Lock()
	close(done)
	r.cancel = nil
	r.done = nil
	r.mu.Unlock()

	log.WithField("tracer", r.name).Info("tracer stopped")
}

func (r *eventRunner) incrementRunCount() {
	r.mu.Lock()
	r.runCount++
	r.mu.Unlock()
}

func (r *eventRunner) stop(ctx context.Context) error {
	done, err := r.cancelRun()
	if err != nil {
		return err
	}

	return waitForRunner(ctx, r.name, done)
}

func (r *eventRunner) cancelRun() (<-chan struct{}, error) {
	r.mu.RLock()
	if r.done == nil {
		r.mu.RUnlock()
		return nil, newTracerStateError(ErrTracerNotRunning, r.name)
	}

	cancel := r.cancel
	done := r.done
	r.mu.RUnlock()

	cancel()

	return done, nil
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
		IsRunning:       r.done != nil,
		RunCount:        r.runCount,
		RestartInterval: int(r.restartInterval / time.Second),
		Roles:           r.roles,
	}
}

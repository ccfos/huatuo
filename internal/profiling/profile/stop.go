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

package profile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"
)

var (
	ErrCollectionDurationElapsed = errors.New("collection duration elapsed")
	ErrStopRequested             = errors.New("collection stop requested")
)

var errTerminationRequested = fmt.Errorf("profiling capture received SIGTERM: %w", context.Canceled)

// RunSignals records a graceful stop separately from session cancellation.
type RunSignals struct {
	stopRequested context.Context
	stopping      atomic.Bool
}

// SubscribeRunSignals subscribes to process signals until the returned cleanup
// function is called. The runner uses the same state to mark the end of collection.
func SubscribeRunSignals(parent context.Context, onSignal func(os.Signal)) (context.Context, *RunSignals, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	listenerCtx, stopListener := context.WithCancel(context.WithoutCancel(parent))
	stopRequested, requestStop := context.WithCancel(listenerCtx)
	runSignals := &RunSignals{stopRequested: stopRequested}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-listenerCtx.Done():
				return
			case sig := <-signals:
				if onSignal != nil {
					onSignal(sig)
				}
				switch {
				case sig == syscall.SIGINT && runSignals.stopping.CompareAndSwap(false, true):
					requestStop()
				case sig == syscall.SIGTERM:
					cancel(errTerminationRequested)
				default:
					cancel(context.Canceled)
				}
			}
		}
	}()
	return ctx, runSignals, func() {
		stopListener()
		<-done
		cancel(nil)
		signal.Stop(signals)
	}
}

// FinalizationTimeout is the cancellation budget for collector finalization and
// output preparation. Synchronous filesystem calls may still delay return.
const FinalizationTimeout = 30 * time.Second

// IsNormalStop accepts only control causes, never an error joined with a failure.
func IsNormalStop(ctx context.Context) bool {
	cause := context.Cause(ctx)
	return cause == ErrCollectionDurationElapsed || cause == ErrStopRequested //nolint:errorlint // Joined failures must not count as control causes.
}

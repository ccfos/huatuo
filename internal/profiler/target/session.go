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

// Package target coordinates independent process profiling sessions.
package target

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Session is the work required to profile one target process.
type Session struct {
	PID int
	Run func(context.Context) error
}

// SessionError identifies which target failed without hiding the cause.
type SessionError struct {
	PID int
	Err error
}

func (e *SessionError) Error() string {
	return fmt.Sprintf("PID %d: %v", e.PID, e.Err)
}

func (e *SessionError) Unwrap() error {
	return e.Err
}

// RunSessions executes every session independently with bounded concurrency.
// A non-positive limit allows all sessions to run concurrently. Context
// cancellation stops scheduling new sessions and is treated as normal
// profiler shutdown; target-specific failures are joined in input order.
func RunSessions(
	ctx context.Context,
	sessions []Session,
	maxConcurrent int,
) error {
	if len(sessions) == 0 {
		return nil
	}
	if maxConcurrent <= 0 || maxConcurrent > len(sessions) {
		maxConcurrent = len(sessions)
	}

	jobs := make(chan int)
	errs := make([]error, len(sessions))

	var workers sync.WaitGroup
	workers.Add(maxConcurrent)
	for range maxConcurrent {
		go func() {
			defer workers.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					continue
				}

				session := sessions[index]
				if session.Run == nil {
					errs[index] = &SessionError{
						PID: session.PID,
						Err: errors.New("target session has no runner"),
					}
					continue
				}

				if err := session.Run(ctx); err != nil &&
					!errors.Is(err, context.Canceled) {
					errs[index] = &SessionError{PID: session.PID, Err: err}
				}
			}
		}()
	}

schedule:
	for index := range sessions {
		if ctx.Err() != nil {
			break
		}
		select {
		case jobs <- index:
		case <-ctx.Done():
			break schedule
		}
	}
	close(jobs)
	workers.Wait()

	return errors.Join(errs...)
}

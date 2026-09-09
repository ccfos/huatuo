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

package target

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

func TestRunSessionsBoundsConcurrency(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	sessions := make([]Session, 4)
	for index := range sessions {
		sessions[index] = Session{
			PID: index + 1,
			Run: func(context.Context) error {
				started <- struct{}{}
				<-release
				return nil
			},
		}
	}

	done := make(chan error, 1)
	go func() {
		done <- RunSessions(context.Background(), sessions, 2)
	}()

	<-started
	<-started
	if got := len(started); got != 0 {
		t.Fatalf("started %d sessions beyond concurrency limit", got)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunSessionsKeepsTargetErrors(t *testing.T) {
	want := errors.New("attach failed")
	err := RunSessions(context.Background(), []Session{
		{PID: 10, Run: func(context.Context) error { return want }},
		{PID: 20, Run: func(context.Context) error { return nil }},
	}, 1)
	if !errors.Is(err, want) {
		t.Fatalf("RunSessions() error = %v, want cause %v", err, want)
	}
	if !strings.Contains(err.Error(), "PID 10") {
		t.Fatalf("RunSessions() error = %q, want target PID", err)
	}
}

func TestRunSessionsStopsSchedulingAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	var secondStarted atomic.Bool

	done := make(chan error, 1)
	go func() {
		done <- RunSessions(ctx, []Session{
			{
				PID: 10,
				Run: func(ctx context.Context) error {
					close(started)
					<-ctx.Done()
					return ctx.Err()
				},
			},
			{
				PID: 20,
				Run: func(context.Context) error {
					secondStarted.Store(true)
					return nil
				},
			},
		}, 1)
	}()

	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if secondStarted.Load() {
		t.Fatal("session started after cancellation")
	}
}

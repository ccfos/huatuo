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

package signal

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

// These tests cover the graceful-shutdown lifecycle around the existing
// context-cancellation test: a termination signal must cancel the run, a
// closed channel must be reported instead of silently exiting, and
// SetupSignals must actually deliver registered signals.

func TestListenSignalAndCancelReturnsSignalAndCancels(t *testing.T) {
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM

	canceled := false
	sig, err := ListenSignalAndCancel(context.Background(), signals, func() { canceled = true })
	if err != nil {
		t.Fatalf("ListenSignalAndCancel() error = %v", err)
	}

	if sig != syscall.SIGTERM {
		t.Fatalf("signal = %v, want SIGTERM", sig)
	}

	if !canceled {
		t.Fatal("cancel was not invoked for a termination signal")
	}
}

func TestListenSignalAndCancelRejectsClosedChannel(t *testing.T) {
	signals := make(chan os.Signal)
	close(signals)

	canceled := false
	sig, err := ListenSignalAndCancel(context.Background(), signals, func() { canceled = true })
	if err == nil {
		t.Fatal("ListenSignalAndCancel() error = nil, want an error for a closed channel")
	}

	if sig != nil {
		t.Fatalf("signal = %v, want nil", sig)
	}

	if canceled {
		t.Fatal("cancel must not run when the signal channel closed unexpectedly")
	}
}

func TestSetupSignalsDeliversTerminationSignals(t *testing.T) {
	signals, err := SetupSignals()
	if err != nil {
		t.Fatalf("SetupSignals() error = %v", err)
	}

	if signals == nil {
		t.Fatal("SetupSignals() channel is nil")
	}

	// Unregister before the test ends so no signal stays intercepted.
	defer StopSignals(signals)

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("kill(SIGTERM) error = %v", err)
	}

	select {
	case sig := <-signals:
		if sig != syscall.SIGTERM {
			t.Fatalf("signal = %v, want SIGTERM", sig)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM was not delivered to the registered channel")
	}
}

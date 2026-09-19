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

package toolstream

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/toolstream/transport"
)

func TestDispatchProcessesDataBeforeCompletingExpectedSession(t *testing.T) {
	server, err := NewServer(t.TempDir() + "/toolstream.sock")
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() error = %v", err)
	}
	var (
		handled    bool
		isExpected bool
	)
	Register(server, "profiler", func(session *Session, event struct {
		Value string `json:"value"`
	},
	) error {
		handled = event.Value == "final"
		isExpected = session.IsExpected
		return nil
	})

	server.dispatch(&transport.Session{
		ToolName: "profiler",
		TaskID:   "job-1",
	}, transport.ChunkMsg{
		Data: []byte(`{"value":"final"}`),
		End:  true,
	})
	if err := server.AwaitSession(t.Context(), "profiler", "job-1"); err != nil {
		t.Fatalf("AwaitSession() error = %v", err)
	}
	if !handled || !isExpected {
		t.Fatalf("handler state = (handled=%t, expected=%t)", handled, isExpected)
	}
}

func TestDispatchCompletesErrorEndForExpectedSession(t *testing.T) {
	server, err := NewServer(t.TempDir() + "/toolstream.sock")
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() error = %v", err)
	}

	server.dispatch(&transport.Session{
		ToolName: "profiler",
		TaskID:   "job-1",
	}, transport.ChunkMsg{Err: "finalization failed", End: true})
	err = server.AwaitSession(t.Context(), "profiler", "job-1")
	if err == nil || !strings.Contains(err.Error(), "finalization failed") {
		t.Fatalf("AwaitSession() error = %v", err)
	}
}

func TestDispatchCompletesHandlerErrorEndForExpectedSession(t *testing.T) {
	server, err := NewServer(t.TempDir() + "/toolstream.sock")
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() error = %v", err)
	}
	handlerErr := errors.New("persist final profile")
	Register(server, "profiler", func(*Session, struct{}) error {
		return handlerErr
	})

	server.dispatch(&transport.Session{
		ToolName: "profiler",
		TaskID:   "job-1",
	}, transport.ChunkMsg{Data: []byte(`{}`), End: true})
	err = server.AwaitSession(t.Context(), "profiler", "job-1")
	if !errors.Is(err, handlerErr) {
		t.Fatalf("AwaitSession() error = %v, want handler error", err)
	}
}

// CancelSession must release waiters that are already blocked on the session
// channel, so a finalize path cannot stall until its context expires.
func TestCancelSessionWakesBlockedAwaitSession(t *testing.T) {
	server, err := NewServer(t.TempDir() + "/toolstream.sock")
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	awaitErr := make(chan error, 1)
	go func() {
		awaitErr <- server.AwaitSession(ctx, "profiler", "job-1")
	}()
	// Let the waiter reach the select before canceling; otherwise the test
	// would only prove that a later AwaitSession observes the cancellation.
	time.Sleep(100 * time.Millisecond)

	start := time.Now()
	server.CancelSession("profiler", "job-1")

	select {
	case err := <-awaitErr:
		if !errors.Is(err, ErrSessionCanceled) {
			t.Fatalf("AwaitSession() error = %v, want ErrSessionCanceled", err)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("AwaitSession() woke after %v, want an immediate wakeup", elapsed)
		}
	case <-ctx.Done():
		t.Fatal("AwaitSession() stayed blocked after CancelSession()")
	}
}

// Closing done broadcasts the cancellation to every waiter; waking a single
// caller would leave the rest of the finalize path sleeping until they time out.
func TestCancelSessionWakesEveryAwaitSessionCaller(t *testing.T) {
	server, err := NewServer(t.TempDir() + "/toolstream.sock")
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() error = %v", err)
	}

	const waiters = 3
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	errCh := make(chan error, waiters)
	for range waiters {
		go func() {
			errCh <- server.AwaitSession(ctx, "profiler", "job-1")
		}()
	}
	time.Sleep(100 * time.Millisecond)
	server.CancelSession("profiler", "job-1")

	for i := range waiters {
		select {
		case err := <-errCh:
			if !errors.Is(err, ErrSessionCanceled) {
				t.Fatalf("waiter %d: AwaitSession() error = %v, want ErrSessionCanceled", i, err)
			}
		case <-ctx.Done():
			t.Fatalf("waiter %d: AwaitSession() stayed blocked after CancelSession()", i)
		}
	}
}

// A canceled expectation must be removed, not merely marked, so the same
// tool/task pair can be retried after a profiler start failure.
func TestCancelSessionAllowsExpectSessionToRegisterAgain(t *testing.T) {
	server, err := NewServer(t.TempDir() + "/toolstream.sock")
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() error = %v", err)
	}

	server.CancelSession("profiler", "job-1")

	// The canceled expectation must be gone rather than merely marked, so a
	// retry of the same tool/task pair registers a fresh stream instead of
	// failing with "already expected".
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() after CancelSession() error = %v", err)
	}

	// Repeated cancels and cancels for unknown keys must stay no-ops: the
	// failure paths call CancelSession without tracking what is registered.
	server.CancelSession("profiler", "job-1")
	server.CancelSession("profiler", "job-1")
	server.CancelSession("profiler", "unknown-job")
	server.CancelSession("unknown-tool", "job-1")
	server.CancelSession("", "")

	// Those cancels released the retry as well, so the key is free again and
	// the next operation starts from a clean slate instead of a stale entry.
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() after repeated CancelSession() error = %v", err)
	}
	server.CancelSession("profiler", "job-1")
}

// CancelSession is called unconditionally from failure paths, so nil servers
// and keys that were never expected must stay safe no-ops.
func TestCancelSessionNilServerAndUnknownKeyAreNoOps(t *testing.T) {
	var nilServer *Server
	nilServer.CancelSession("profiler", "job-1")

	server, err := NewServer(t.TempDir() + "/toolstream.sock")
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	server.CancelSession("profiler", "job-1")
	if err := server.AwaitSession(t.Context(), "profiler", "job-1"); err == nil {
		t.Fatal("AwaitSession() after cancel of unknown session = nil, want error")
	}
}

// finishSession keeps its entry until AwaitSession collects the error, so a
// cancel arriving after the stream ended still has to release the key.
func TestCancelSessionReleasesFinishedExpectation(t *testing.T) {
	server, err := NewServer(t.TempDir() + "/toolstream.sock")
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() error = %v", err)
	}
	server.dispatch(&transport.Session{
		ToolName: "profiler",
		TaskID:   "job-1",
	}, transport.ChunkMsg{Data: []byte(`{}`), End: true})

	server.CancelSession("profiler", "job-1")

	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() after CancelSession() of finished session error = %v", err)
	}
	server.CancelSession("profiler", "job-1")
}

// A straggler end frame from a canceled process must not close done a second
// time nor recreate the expectation for the next operation.
func TestDispatchEndAfterCancelSessionDoesNotRestoreExpectation(t *testing.T) {
	server, err := NewServer(t.TempDir() + "/toolstream.sock")
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() error = %v", err)
	}
	Register(server, "profiler", func(*Session, struct{}) error { return nil })

	server.CancelSession("profiler", "job-1")
	server.dispatch(&transport.Session{
		ToolName: "profiler",
		TaskID:   "job-1",
	}, transport.ChunkMsg{Data: []byte(`{}`), End: true})

	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() after straggler end error = %v", err)
	}
	server.CancelSession("profiler", "job-1")
}

// AwaitSession releases only the session it observed; a cancel carried by a
// timed-out waiter must not terminate a retry registered under the same key.
func TestStaleCancelDoesNotReleaseReplacementExpectation(t *testing.T) {
	server, err := NewServer(t.TempDir() + "/toolstream.sock")
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() error = %v", err)
	}

	key := sessionKey{"profiler", "job-1"}
	server.sessionsMu.Lock()
	stale := server.sessions[key]
	server.sessionsMu.Unlock()

	// Simulate the timeout path racing with a retry: the first expectation is
	// replaced before the timed-out waiter releases the one it observed.
	server.CancelSession("profiler", "job-1")
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() after CancelSession() error = %v", err)
	}
	server.cancelSession(key, stale)

	if err := server.ExpectSession("profiler", "job-1"); err == nil ||
		!strings.Contains(err.Error(), "already expected") {
		t.Fatalf("ExpectSession() after stale cancel = %v, want already expected", err)
	}

	// The replacement is still registered for the next waiter, so canceling it
	// behaves exactly like canceling a fresh expectation.
	server.CancelSession("profiler", "job-1")
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() after replacement cancel error = %v", err)
	}
	server.CancelSession("profiler", "job-1")
}

func TestAwaitSessionCancellationRemovesExpectation(t *testing.T) {
	server, err := NewServer(t.TempDir() + "/toolstream.sock")
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := server.AwaitSession(ctx, "profiler", "job-1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("AwaitSession() error = %v, want context.Canceled", err)
	}
	if err := server.ExpectSession("profiler", "job-1"); err != nil {
		t.Fatalf("ExpectSession() after cancellation error = %v", err)
	}
}

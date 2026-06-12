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

package toolstream_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"huatuo-bamai/internal/toolstream"
)

type testEvent struct {
	ID    int    `json:"id"`
	Value string `json:"value"`
}

type capturedEvent struct {
	sess  toolstream.Session
	event testEvent
}

type eventRecorder struct {
	mu       sync.Mutex
	events   []capturedEvent
	notifyCh chan struct{}
}

func newRecorder() *eventRecorder {
	return &eventRecorder{notifyCh: make(chan struct{}, 10)}
}

func (r *eventRecorder) handler(sess *toolstream.Session, ev testEvent) error {
	r.mu.Lock()
	r.events = append(r.events, capturedEvent{sess: *sess, event: ev})
	r.mu.Unlock()

	select {
	case r.notifyCh <- struct{}{}:
	default:
	}

	return nil
}

func (r *eventRecorder) waitN(t *testing.T, n int) []capturedEvent {
	t.Helper()

	deadline := time.After(3 * time.Second)

	for {
		r.mu.Lock()
		got := make([]capturedEvent, len(r.events))
		copy(got, r.events)
		r.mu.Unlock()

		if len(got) >= n {
			return got[:n]
		}

		select {
		case <-r.notifyCh:
		case <-deadline:
			t.Fatalf("timeout waiting for %d events; got %d", n, len(got))
		}
	}
}

func newTestServer(t *testing.T) (*toolstream.Server, string) {
	t.Helper()

	sockPath := t.TempDir() + "/test.sock"

	srv, err := toolstream.NewServer(sockPath)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}

	return srv, sockPath
}

func TestRegisterAndReceive(t *testing.T) {
	srv, sockPath := newTestServer(t)
	rec := newRecorder()

	toolstream.Register(srv, "testtool", rec.handler)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() { _ = srv.Close() })

	c, err := toolstream.NewClient(toolstream.ClientOptions{
		SockPath: sockPath,
		ToolName: "testtool",
		Version:  "1.0",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	defer c.End()

	events := []testEvent{
		{ID: 1, Value: "alpha"},
		{ID: 2, Value: "beta"},
		{ID: 3, Value: "gamma"},
	}

	for _, ev := range events {
		if err := c.Send(ev); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	got := rec.waitN(t, 3)

	for i, g := range got {
		if g.event.ID != events[i].ID {
			t.Errorf("event %d: ID=%d want %d", i, g.event.ID, events[i].ID)
		}

		if g.event.Value != events[i].Value {
			t.Errorf("event %d: Value=%q want %q", i, g.event.Value, events[i].Value)
		}

		if g.sess.ToolName != "testtool" {
			t.Errorf("event %d: ToolName=%q want %q", i, g.sess.ToolName, "testtool")
		}
	}
}

func TestUnregisteredToolIsIgnored(t *testing.T) {
	srv, sockPath := newTestServer(t)
	rec := newRecorder()

	toolstream.Register(srv, "known-tool", rec.handler)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() { _ = srv.Close() })

	// Connect as "unknown-tool" which has no handler registered.
	c, err := toolstream.NewClient(toolstream.ClientOptions{
		SockPath: sockPath,
		ToolName: "unknown-tool",
		Version:  "1.0",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_ = c.Send(testEvent{ID: 99})
	c.End()

	// Give the server a moment to process, then assert no handler was called.
	time.Sleep(100 * time.Millisecond)

	rec.mu.Lock()
	n := len(rec.events)
	rec.mu.Unlock()

	if n != 0 {
		t.Errorf("unknown tool handler calls: got %d, want 0", n)
	}
}

func TestMultipleChunksThenEnd(t *testing.T) {
	srv, sockPath := newTestServer(t)
	rec := newRecorder()

	toolstream.Register(srv, "sender", rec.handler)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() { _ = srv.Close() })

	c, err := toolstream.NewClient(toolstream.ClientOptions{
		SockPath: sockPath,
		ToolName: "sender",
		Version:  "2.0",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	for i := range 5 {
		if err := c.Send(testEvent{ID: i}); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}

	c.End() // sends end frame + closes connection

	got := rec.waitN(t, 5)

	for i, g := range got {
		if g.event.ID != i {
			t.Errorf("event %d: ID=%d want %d", i, g.event.ID, i)
		}
	}
}

func TestStartNilServer(t *testing.T) {
	var srv *toolstream.Server
	if err := srv.Start(); !errors.Is(err, toolstream.ErrNotInitialized) {
		t.Errorf("Start on nil server: got %v, want ErrNotInitialized", err)
	}
}

func TestStartZeroValueServer(t *testing.T) {
	srv := &toolstream.Server{}
	if err := srv.Start(); !errors.Is(err, toolstream.ErrNotInitialized) {
		t.Errorf("Start on zero-value server: got %v, want ErrNotInitialized", err)
	}
}

func TestStartAlreadyStarted(t *testing.T) {
	srv, _ := newTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	t.Cleanup(func() { _ = srv.Close() })

	if err := srv.Start(); !errors.Is(err, toolstream.ErrAlreadyStarted) {
		t.Errorf("second Start: got %v, want ErrAlreadyStarted", err)
	}
}

func TestCloseNilServer(t *testing.T) {
	var srv *toolstream.Server
	if err := srv.Close(); !errors.Is(err, toolstream.ErrNotInitialized) {
		t.Errorf("Close on nil server: got %v, want ErrNotInitialized", err)
	}
}

func TestCloseBeforeStart(t *testing.T) {
	srv, _ := newTestServer(t)
	if err := srv.Close(); err != nil {
		t.Errorf("Close before Start: got %v, want nil", err)
	}
}

func TestCloseIdempotent(t *testing.T) {
	srv, _ := newTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() { _ = srv.Close() })

	if err := srv.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	for range 3 {
		if err := srv.Close(); err != nil {
			t.Errorf("repeated Close: got %v, want nil", err)
		}
	}
}

func TestStartAfterClose(t *testing.T) {
	srv, sockPath := newTestServer(t)
	rec := newRecorder()

	toolstream.Register(srv, "restart-tool", rec.handler)

	if err := srv.Start(); err != nil {
		t.Fatalf("first Start: %v", err)
	}

	t.Cleanup(func() { _ = srv.Close() })

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := srv.Start(); err != nil {
		t.Fatalf("second Start after Close: %v", err)
	}

	c, err := toolstream.NewClient(toolstream.ClientOptions{
		SockPath: sockPath,
		ToolName: "restart-tool",
		Version:  "1.0",
	})
	if err != nil {
		t.Fatalf("NewClient after restart: %v", err)
	}

	defer c.End()

	if err := c.Send(testEvent{ID: 42, Value: "after-restart"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	got := rec.waitN(t, 1)
	if got[0].event.ID != 42 {
		t.Errorf("restarted server: event ID got %d, want 42", got[0].event.ID)
	}
}

func TestNewServerDefaultSingleton(t *testing.T) {
	s1, err := toolstream.NewServerDefault()
	if err != nil {
		t.Fatalf("first NewServerDefault: %v", err)
	}

	s2, err := toolstream.NewServerDefault()
	if err != nil {
		t.Fatalf("second NewServerDefault: %v", err)
	}

	if s1 != s2 {
		t.Error("NewServerDefault: returned different pointers, want singleton")
	}
}

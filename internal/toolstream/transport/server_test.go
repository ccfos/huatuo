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

package transport

import (
	"net"
	"sync"
	"testing"
	"time"

	capnp "capnproto.org/go/capnp/v3"
)

// recordedChunk captures the data passed to the mock handler.
type recordedChunk struct {
	Session Session
	Data    []byte
	Flush   bool
	End     bool
	Error   string
}

type recorder struct {
	mu       sync.Mutex
	captured []recordedChunk
	notify   chan struct{}
}

func (r *recorder) handler(sess *Session, chunk ChunkMsg) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.captured = append(r.captured, recordedChunk{
		Session: *sess,
		Data:    append([]byte(nil), chunk.Data...),
		Flush:   chunk.Flush,
		End:     chunk.End,
		Error:   chunk.Err,
	})
	if r.notify != nil {
		select {
		case r.notify <- struct{}{}:
		default:
		}
	}
}

func (r *recorder) snapshot() []recordedChunk {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedChunk, len(r.captured))
	copy(out, r.captured)
	return out
}

// newPipedServer wires a Server with a recorder, runs handleConn directly on
// one side of a net.Pipe, and returns the raw client net.Conn (for writing
// raw bytes) and a capnp.Encoder wrapping the same conn (for sending frames).
// The returned wait func blocks until handleConn exits.
func newPipedServer(t *testing.T) (rawClient net.Conn, clientEnc *capnp.Encoder, rec *recorder, wait func()) {
	t.Helper()
	c1, c2 := net.Pipe()
	rec = &recorder{}
	srv := &Server{
		connections: make(map[net.Conn]struct{}),
		handler:     rec.handler,
	}
	done := make(chan struct{})
	go func() {
		srv.handleConn(t.Context(), c2)
		close(done)
	}()
	return c1, capnp.NewEncoder(c1), rec, func() {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("handleConn did not exit within 2s")
		}
	}
}

// sendConnect sends a Connect frame via enc.
func sendConnect(t *testing.T, enc *capnp.Encoder, toolName, version, taskID string) {
	t.Helper()
	m, seg, err := capnp.NewMessage(capnp.SingleSegment(nil))
	if err != nil {
		t.Fatalf("send connect new message: %v", err)
	}
	root, err := NewRootMessage(seg)
	if err != nil {
		t.Fatalf("send connect new root: %v", err)
	}
	c, err := root.NewConnect()
	if err != nil {
		t.Fatalf("send connect new connect: %v", err)
	}
	if err := c.SetToolName(toolName); err != nil {
		t.Fatalf("send connect set tool name: %v", err)
	}
	if err := c.SetVersion(version); err != nil {
		t.Fatalf("send connect set version: %v", err)
	}
	if err := c.SetTaskID(taskID); err != nil {
		t.Fatalf("send connect set task id: %v", err)
	}
	c.SetProtoVersion(1)
	if err := enc.Encode(m); err != nil {
		t.Fatalf("send connect encode: %v", err)
	}
}

// sendChunk sends a Chunk frame via enc.
func sendChunk(t *testing.T, enc *capnp.Encoder, data []byte, flush, end bool, errStr string) {
	t.Helper()
	m, seg, err := capnp.NewMessage(capnp.SingleSegment(nil))
	if err != nil {
		t.Fatalf("send chunk new message: %v", err)
	}
	root, err := NewRootMessage(seg)
	if err != nil {
		t.Fatalf("send chunk new root: %v", err)
	}
	chunk, err := root.NewChunk()
	if err != nil {
		t.Fatalf("send chunk new chunk: %v", err)
	}
	if len(data) > 0 {
		if err := chunk.SetData(data); err != nil {
			t.Fatalf("send chunk set data: %v", err)
		}
	}
	chunk.SetFlush(flush)
	chunk.SetEnd(end)
	if errStr != "" {
		if err := chunk.SetError(errStr); err != nil {
			t.Fatalf("send chunk set error: %v", err)
		}
	}
	if err := enc.Encode(m); err != nil {
		t.Fatalf("send chunk encode: %v", err)
	}
}

func TestNormalPath(t *testing.T) {
	rawClient, clientEnc, rec, wait := newPipedServer(t)

	go func() {
		sendConnect(t, clientEnc, "dropwatch", "1.0", "")
		sendChunk(t, clientEnc, []byte(`{"container_id":"c1","x":1}`), true, false, "")
		sendChunk(t, clientEnc, []byte(`{"container_id":"c2","x":2}`), true, false, "")
		sendChunk(t, clientEnc, nil, false, true, "")
		rawClient.Close()
	}()

	wait()
	got := rec.snapshot()
	if len(got) != 3 {
		t.Fatalf("want 3 handler calls, got %d", len(got))
	}
	for i, c := range got {
		if c.Session.ToolName != "dropwatch" {
			t.Errorf("call %d ToolName=%q want %q", i, c.Session.ToolName, "dropwatch")
		}
		if c.Session.Version != "1.0" {
			t.Errorf("call %d Version=%q want %q", i, c.Session.Version, "1.0")
		}
	}
	if !got[2].End {
		t.Errorf("third call End=false want true")
	}
	if len(got[0].Data) == 0 || len(got[1].Data) == 0 {
		t.Errorf("data chunks should have non-empty Data")
	}
}

func TestErrorEnd(t *testing.T) {
	rawClient, clientEnc, rec, wait := newPipedServer(t)

	go func() {
		sendConnect(t, clientEnc, "tool", "v", "")
		sendChunk(t, clientEnc, nil, false, true, "boom")
		rawClient.Close()
	}()

	wait()
	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("want 1 handler call, got %d", len(got))
	}
	if got[0].Error != "boom" {
		t.Errorf("Error=%q want %q", got[0].Error, "boom")
	}
	if !got[0].End {
		t.Errorf("End=false want true")
	}
}

func TestDataAndEndCombined(t *testing.T) {
	rawClient, clientEnc, rec, wait := newPipedServer(t)

	go func() {
		sendConnect(t, clientEnc, "tool", "v", "")
		sendChunk(t, clientEnc, []byte(`{"container_id":"c"}`), true, true, "")
		rawClient.Close()
	}()

	wait()
	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("want 1 handler call, got %d", len(got))
	}
	if !got[0].End {
		t.Errorf("End=false want true")
	}
	if len(got[0].Data) == 0 {
		t.Errorf("Data should not be empty")
	}
}

func TestUnexpectedClose(t *testing.T) {
	rawClient, clientEnc, rec, wait := newPipedServer(t)

	go func() {
		sendConnect(t, clientEnc, "tool", "v", "")
		sendChunk(t, clientEnc, []byte(`{"container_id":"c"}`), true, false, "")
		rawClient.Close()
	}()

	wait()
	got := rec.snapshot()
	if len(got) != 1 {
		t.Fatalf("want 1 handler call, got %d", len(got))
	}
}

func TestInvalidFirstFrame(t *testing.T) {
	rawClient, clientEnc, rec, wait := newPipedServer(t)

	go func() {
		// Send a chunk frame instead of a connect frame.
		sendChunk(t, clientEnc, []byte("payload"), true, false, "")
		rawClient.Close()
	}()

	wait()
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("want 0 handler calls, got %d", len(got))
	}
}

func TestEmptyToolName(t *testing.T) {
	rawClient, clientEnc, rec, wait := newPipedServer(t)

	go func() {
		sendConnect(t, clientEnc, "", "v", "")
		rawClient.Close()
	}()

	wait()
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("want 0 handler calls, got %d", len(got))
	}
}

func TestDecodeFailureAfterConnect(t *testing.T) {
	// rawClient gives direct access to the underlying conn, bypassing the
	// Cap'n Proto encoder, so we can write malformed frame bytes.
	rawClient, clientEnc, rec, wait := newPipedServer(t)

	go func() {
		sendConnect(t, clientEnc, "tool", "v", "")
		// Write a malformed Cap'n Proto segment header; decoder will fail.
		_, _ = rawClient.Write([]byte{0xff, 0xff, 0xff, 0xff}) //nolint:errcheck
		rawClient.Close()
	}()

	wait()
	// 0 handler calls; decode error is logged and connection is dropped.
	if got := rec.snapshot(); len(got) != 0 {
		t.Fatalf("want 0 handler calls, got %d", len(got))
	}
}

// TestClientRoundTrip verifies that newClient + SendChunk + SendEnd round-trip
// produces the expected handler calls through a real UDS socket.
func TestClientRoundTrip(t *testing.T) {
	dir := t.TempDir()
	sockPath := dir + "/test.sock"

	rec := &recorder{notify: make(chan struct{}, 10)}
	l, err := ListenUDS(sockPath)
	if err != nil {
		t.Fatalf("ListenUDS: %v", err)
	}

	srv, err := Serve(l, rec.handler)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}

	defer func() { _ = srv.Close() }()

	var c *Client

	for range 20 {
		c, err = NewClient(sockPath, "client-tool", "9.9", "task-rt")
		if err == nil {
			break
		}

		time.Sleep(5 * time.Millisecond)
	}

	if c == nil {
		t.Fatalf("NewClient: %v", err)
	}

	if err := c.SendChunk([]byte(`{"container_id":"abc","k":"v"}`), true); err != nil {
		t.Fatalf("SendChunk: %v", err)
	}
	if err := c.SendEnd(); err != nil {
		t.Fatalf("SendEnd: %v", err)
	}
	_ = c.Close()

	for i := 0; i < 2; i++ {
		select {
		case <-rec.notify:
		case <-time.After(2 * time.Second):
			t.Fatalf("handler call %d not received within 2s", i+1)
		}
	}

	got := rec.snapshot()
	if len(got) != 2 {
		t.Fatalf("want 2 handler calls, got %d", len(got))
	}
	if got[0].Session.ToolName != "client-tool" {
		t.Errorf("ToolName=%q want %q", got[0].Session.ToolName, "client-tool")
	}
	if got[0].Session.Version != "9.9" {
		t.Errorf("Version=%q want %q", got[0].Session.Version, "9.9")
	}
	if got[0].Session.TaskID != "task-rt" {
		t.Errorf("TaskID=%q want %q", got[0].Session.TaskID, "task-rt")
	}
	if string(got[0].Data) != `{"container_id":"abc","k":"v"}` {
		t.Errorf("Data=%q unexpected", string(got[0].Data))
	}
	if !got[0].Flush {
		t.Errorf("first chunk Flush=false want true")
	}
	if !got[1].End {
		t.Errorf("second chunk End=false want true")
	}
}

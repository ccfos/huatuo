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

// Package vsock implements the Sandbox write-only tracing Storage backend.
package vsock

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	capnp "capnproto.org/go/capnp/v3"

	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/internal/toolstream/transport"
)

func waitConnected(t *testing.T, b *Backend) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		b.mutex.Lock()
		ready := b.connected
		b.mutex.Unlock()
		if ready {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("sender did not connect")
}

func TestQueueAndCloseDrain(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan string, 4)
	server, err := transport.Serve(listener, func(session *transport.Session, chunk transport.ChunkMsg) {
		if session.ToolName != TracingTool {
			t.Errorf("tool=%q", session.ToolName)
		}
		if len(chunk.Data) > 0 {
			received <- string(chunk.Data)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	b, err := New(Options{ToolName: TracingTool, DialContext: func(ctx context.Context, _ uint32) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	}})
	if err != nil {
		t.Fatal(err)
	}
	waitConnected(t, b)
	data := []byte(`{"tracer_id":"guest-observation"}`)
	if err := b.Save(context.Background(), driver.Record{Data: data}, driver.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	data[0] = '!'
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got != `{"tracer_id":"guest-observation"}` {
			t.Errorf("received %q", got)
		}
	case <-ctx.Done():
		t.Fatal("queued document was not drained")
	}
	if err := b.Save(ctx, driver.Record{Data: data}, driver.SaveOptions{}); !errors.Is(err, ErrClosed) {
		t.Errorf("save after close=%v", err)
	}
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestQueueLimitsAndUnsupportedGuarantees(t *testing.T) {
	b := &Backend{options: Options{MaxMessageSize: 128, MaxPendingBytes: 128}, connected: true, notify: make(chan struct{}, 1)}
	rec := driver.Record{Data: []byte(`{"value":"bounded"}`)}
	if err := b.Save(context.Background(), rec, driver.SaveOptions{WaitForVisibility: true}); !errors.Is(err, driver.ErrUnsupportedOp) {
		t.Errorf("visibility=%v", err)
	}
	if err := b.Save(context.Background(), rec, driver.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(context.Background(), rec, driver.SaveOptions{}); err == nil {
		t.Error("queue overflow accepted")
	}
	if err := b.Save(context.Background(), driver.Record{Data: make([]byte, 129)}, driver.SaveOptions{}); err == nil {
		t.Error("oversized message accepted")
	}
	b.connected = false
	if err := b.Save(context.Background(), rec, driver.SaveOptions{}); err == nil {
		t.Error("disconnected message accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := b.Save(ctx, rec, driver.SaveOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancellation=%v", err)
	}
}

func TestCloseCancelsHandshakeAndDial(t *testing.T) {
	for _, handshake := range []bool{false, true} {
		t.Run(map[bool]string{false: "dial", true: "handshake"}[handshake], func(t *testing.T) {
			entered := make(chan struct{})
			b, err := New(Options{ToolName: TracingTool, DialContext: func(ctx context.Context, _ uint32) (net.Conn, error) {
				close(entered)
				if !handshake {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				client, server := net.Pipe()
				t.Cleanup(func() { _ = server.Close() })
				return client, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			<-entered
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if err := b.Close(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCloseDeadlineInterruptsBlockedSend(t *testing.T) {
	release := make(chan struct{})
	b, err := New(Options{ToolName: TracingTool, DialContext: func(_ context.Context, _ uint32) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer server.Close()
			message, err := capnp.NewDecoder(server).Decode()
			if err == nil {
				message.Release()
			}
			<-release
		}()
		return client, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer close(release)
	waitConnected(t, b)
	if err := b.Save(context.Background(), driver.Record{Data: []byte(`{"value":1}`)}, driver.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := b.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("close=%v", err)
	}
}

func TestReconnectAndConcurrentSave(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server, err := transport.Serve(listener, func(*transport.Session, transport.ChunkMsg) {})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	var mutex sync.Mutex
	attempts := 0
	b, err := New(Options{ToolName: ProfilingTool, RetryInterval: time.Millisecond, DialContext: func(ctx context.Context, _ uint32) (net.Conn, error) {
		mutex.Lock()
		attempts++
		attempt := attempts
		mutex.Unlock()
		if attempt == 1 {
			return nil, errors.New("host restarting")
		}
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	}})
	if err != nil {
		t.Fatal(err)
	}
	waitConnected(t, b)
	var writers sync.WaitGroup
	for range 16 {
		writers.Add(1)
		go func() {
			defer writers.Done()
			_ = b.Save(context.Background(), driver.Record{Data: []byte(`{"value":1}`)}, driver.SaveOptions{})
		}()
	}
	writers.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := b.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

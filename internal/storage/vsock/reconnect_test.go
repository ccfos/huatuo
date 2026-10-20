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
	"net"
	"sync/atomic"
	"testing"
	"time"

	capnp "capnproto.org/go/capnp/v3"

	"github.com/ccfos/huatuo/internal/storage/driver"
	"github.com/ccfos/huatuo/internal/toolstream/transport"
)

func TestReconnectAfterBrokenStream(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan string, 1)
	server, err := transport.Serve(listener, func(_ *transport.Session, chunk transport.ChunkMsg) {
		if len(chunk.Data) > 0 {
			received <- string(chunk.Data)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	breakFirst := make(chan struct{})
	firstClosed := make(chan struct{})
	var attempts atomic.Int32
	b, err := New(Options{ToolName: TracingTool, RetryInterval: time.Millisecond, DialContext: func(
		ctx context.Context,
		_ uint32,
	) (net.Conn, error) {
		if attempts.Add(1) > 1 {
			return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
		}
		client, peer := net.Pipe()
		go func() {
			message, err := capnp.NewDecoder(peer).Decode()
			if err == nil {
				message.Release()
			}
			<-breakFirst
			_ = peer.Close()
			close(firstClosed)
		}()
		return client, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	defer b.Close(ctx)
	waitConnected(t, b)
	close(breakFirst)
	<-firstClosed
	if err := b.Save(ctx, driver.Record{Data: []byte(`{"lost":true}`)}, driver.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for attempts.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if attempts.Load() < 2 {
		t.Fatal("broken stream was not reconnected")
	}
	waitConnected(t, b)
	if err := b.Save(ctx, driver.Record{Data: []byte(`{"restored":true}`)}, driver.SaveOptions{}); err != nil {
		t.Fatal(err)
	}
	select {
	case value := <-received:
		if value != `{"restored":true}` {
			t.Errorf("received=%s", value)
		}
	case <-ctx.Done():
		t.Fatal("reconnected stream did not deliver")
	}
}

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
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	capnp "capnproto.org/go/capnp/v3"
)

func TestSendDoesNotOwnConnectionCleanup(t *testing.T) {
	writeErr := errors.New("write failed")
	closeErr := errors.New("close failed")
	conn := &failingConn{
		writeErr: writeErr,
		closeErr: closeErr,
	}
	client := &Client{
		encoder: capnp.NewEncoder(conn),
		conn:    conn,
	}
	err := client.Send(nil, SendOptions{End: true})
	if !errors.Is(err, writeErr) {
		t.Fatalf("Send: %v", err)
	}
	if conn.closeCalls != 0 {
		t.Fatal("Send closed caller-owned connection")
	}
	if err := client.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close: %v", err)
	}
	if err := client.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("repeated Close: %v", err)
	}

	var closers sync.WaitGroup
	for range 20 {
		closers.Add(1)
		go func() {
			defer closers.Done()
			if err := client.Close(); !errors.Is(err, closeErr) {
				t.Errorf("concurrent Close: %v", err)
			}
		}()
	}
	closers.Wait()

	if conn.closeCalls != 1 {
		t.Fatalf("Close calls=%d", conn.closeCalls)
	}
}

type failingConn struct {
	writeErr   error
	closeErr   error
	closeCalls int
}

func (*failingConn) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (c *failingConn) Write([]byte) (int, error) {
	return 0, c.writeErr
}

func (c *failingConn) Close() error {
	c.closeCalls++
	return c.closeErr
}

func (*failingConn) LocalAddr() net.Addr {
	return nil
}

func (*failingConn) RemoteAddr() net.Addr {
	return nil
}

func (*failingConn) SetDeadline(time.Time) error {
	return nil
}

func (*failingConn) SetReadDeadline(time.Time) error {
	return nil
}

func (*failingConn) SetWriteDeadline(time.Time) error {
	return nil
}

func TestCloseInterruptsBlockedWrite(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	client := &Client{
		encoder: capnp.NewEncoder(local),
		conn:    local,
	}
	done := make(chan error, 1)
	go func() { done <- client.Send([]byte("payload"), SendOptions{}) }()
	if err := remote.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// Reading just one byte leaves the frame write blocked on the pipe.
	if _, err := io.ReadFull(remote, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("Send: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock Send")
	}
}

// TestClientMetadataHandshake 验证带与不带 context 的连接构造函数，
// 均传递完整工具/任务元数据并使用当前协议版本，且数据与结束帧可以正常接收。
func TestClientMetadataHandshake(t *testing.T) {
	for _, tc := range []struct {
		name    string
		connect func(context.Context, net.Conn, ClientMetadata) (*Client, error)
	}{
		{"connection", func(_ context.Context, conn net.Conn, metadata ClientMetadata) (*Client, error) {
			return NewClientWithConn(conn, metadata)
		}},
		{"context", NewClientWithConnContext},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn, _, received, wait := newPipedServer(t)
			t.Cleanup(func() { _ = conn.Close(); wait() })
			metadata := ClientMetadata{ToolName: "checkout-profiler", Version: "2.3.0", TaskID: "checkout-profile"}
			client, err := tc.connect(t.Context(), conn, metadata)
			if err != nil {
				t.Fatal(err)
			}
			if err := client.SendChunk([]byte("checkout-sample"), false); err != nil {
				t.Fatal(err)
			}
			if err := client.End(); err != nil {
				t.Fatal(err)
			}
			wait()
			chunks := received.snapshot()
			if len(chunks) != 2 || string(chunks[0].Data) != "checkout-sample" || !chunks[1].End {
				t.Fatalf("unexpected frames: %+v", chunks)
			}
			session := chunks[0].Session
			if session.ToolName != metadata.ToolName || session.Version != metadata.Version ||
				session.TaskID != metadata.TaskID || session.ProtoVersion != ProtocolVersion {
				t.Fatalf("handshake metadata=%+v", session)
			}
		})
	}
}

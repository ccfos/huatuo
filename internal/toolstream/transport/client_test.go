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
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	capnp "capnproto.org/go/capnp/v3"
)

func TestSendDoesNotOwnConnectionCleanup(t *testing.T) {
	writeErr := errors.New("write failed")
	closeErr := errors.New("close failed")
	conn := &failingConn{writeErr: writeErr, closeErr: closeErr}
	client := &Client{encoder: capnp.NewEncoder(conn), conn: conn}
	err := client.Send(t.Context(), nil, SendOptions{End: true})
	if !errors.Is(err, writeErr) {
		t.Fatalf("Send: %v", err)
	}
	if conn.closeCalls != 0 {
		t.Fatal("Send closed caller-owned connection")
	}
	if err := client.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("Close: %v", err)
	}
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

func TestSendCancellationInterruptsBlockedWrite(t *testing.T) {
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	client := &Client{encoder: capnp.NewEncoder(local), conn: local}
	deadline, stop := context.WithTimeout(t.Context(), time.Second)
	defer stop()
	ctx, cancel := context.WithCancel(deadline)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- client.Send(ctx, []byte("payload"), SendOptions{}) }()
	if err := remote.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// Reading just one byte leaves the frame write blocked on the pipe.
	if _, err := io.ReadFull(remote, make([]byte, 1)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Send: %v", err)
		}
	case <-deadline.Done():
		t.Fatal("cancellation did not unblock Send")
	}
}

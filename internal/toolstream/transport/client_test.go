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
	"testing"
	"time"

	capnp "capnproto.org/go/capnp/v3"
)

func TestClientEndReturnsSendAndCloseErrors(t *testing.T) {
	t.Parallel()

	writeErr := errors.New("write failed")
	closeErr := errors.New("close failed")
	conn := &failingConn{writeErr: writeErr, closeErr: closeErr}
	client := &Client{encoder: capnp.NewEncoder(conn), conn: conn}

	err := client.End()
	if !errors.Is(err, writeErr) || !errors.Is(err, closeErr) {
		t.Fatalf("End() error = %v, want write and close errors", err)
	}
	if conn.closeCalls != 1 {
		t.Fatalf("Close() calls = %d, want 1", conn.closeCalls)
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

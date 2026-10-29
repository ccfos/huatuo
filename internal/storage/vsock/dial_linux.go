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
//go:build linux

package vsock

import (
	"context"
	"net"

	"github.com/mdlayher/socket"
	mdvsock "github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"
)

type guestConn struct {
	*socket.Conn
	port uint32
}

func (*guestConn) LocalAddr() net.Addr { return &mdvsock.Addr{} }
func (c *guestConn) RemoteAddr() net.Addr {
	return &mdvsock.Addr{ContextID: mdvsock.Host, Port: c.port}
}

func dialHost(ctx context.Context, port uint32) (net.Conn, error) {
	c, err := socket.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0, "vsock", nil)
	if err != nil {
		return nil, err
	}
	if _, err = c.Connect(ctx, &unix.SockaddrVM{CID: mdvsock.Host, Port: port}); err != nil {
		_ = c.Close()
		return nil, err
	}
	return &guestConn{Conn: c, port: port}, nil
}

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

package guestbridge

import (
	"fmt"
	"net"
	"sync"
	"syscall"

	"github.com/prometheus/client_golang/prometheus"
)

type connectionLimiter struct {
	slots       chan struct{}
	connections prometheus.Gauge
}

func newConnectionLimiter(limit int, connections prometheus.Gauge) *connectionLimiter {
	return &connectionLimiter{
		slots:       make(chan struct{}, limit),
		connections: connections,
	}
}

func (limiter *connectionLimiter) wrap(listener net.Listener) net.Listener {
	return &limitedListener{Listener: listener, limiter: limiter}
}

type limitedListener struct {
	net.Listener
	limiter *connectionLimiter
}

func (listener *limitedListener) Accept() (net.Conn, error) {
	for {
		connection, err := listener.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case listener.limiter.slots <- struct{}{}:
			listener.limiter.connections.Inc()
			return &limitedConnection{
				Conn:    connection,
				limiter: listener.limiter,
			}, nil
		default:
			_ = connection.Close()
		}
	}
}

type limitedConnection struct {
	net.Conn
	limiter *connectionLimiter
	once    sync.Once
}

func (connection *limitedConnection) Close() error {
	err := connection.Conn.Close()
	connection.once.Do(func() {
		<-connection.limiter.slots
		connection.limiter.connections.Dec()
	})
	return err
}

func (connection *limitedConnection) SyscallConn() (syscall.RawConn, error) {
	conn, ok := connection.Conn.(syscall.Conn)
	if !ok {
		return nil, fmt.Errorf("guestbridge: peer credentials unavailable")
	}
	return conn.SyscallConn()
}

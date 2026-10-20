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
	"sync"
	"testing"
	"time"
)

type delayedAcceptListener struct {
	net.Listener
	connection net.Conn
	accepted   chan struct{}
	release    chan struct{}
	closed     chan struct{}
	closeOnce  sync.Once
}

func (listener *delayedAcceptListener) Accept() (net.Conn, error) {
	close(listener.accepted)
	<-listener.release
	return listener.connection, nil
}

func (listener *delayedAcceptListener) Close() error {
	listener.closeOnce.Do(func() { close(listener.closed) })
	return nil
}

// TestShutdownRejectsLateAcceptedConnection verifies the race between shutdown and Accept returning a connection:
// the first Shutdown may time out; a late connection is closed without entering the handler, and a second Shutdown
// waits for actual cleanup.
func TestShutdownRejectsLateAcceptedConnection(t *testing.T) {
	clientConnection, serverConnection := net.Pipe()
	t.Cleanup(func() { _ = clientConnection.Close() })
	t.Cleanup(func() { _ = serverConnection.Close() })
	listener := &delayedAcceptListener{
		connection: serverConnection,
		accepted:   make(chan struct{}),
		release:    make(chan struct{}),
		closed:     make(chan struct{}),
	}
	unblock := sync.OnceFunc(func() { close(listener.release) })
	t.Cleanup(unblock)
	server, err := Serve(listener, func(*Session, ChunkMsg) {
		t.Errorf("handler received a connection accepted after shutdown")
	})
	if err != nil {
		t.Errorf("Serve: %v", err)
		return
	}
	t.Cleanup(func() {
		unblock()
		_ = clientConnection.Close()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			t.Errorf("cleanup Shutdown: %v", err)
		}
	})
	select {
	case <-listener.accepted:
	case <-time.After(time.Second):
		t.Errorf("Accept did not start")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := server.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown error=%v, want deadline exceeded", err)
	}
	select {
	case <-listener.closed:
	default:
		t.Errorf("Shutdown did not close the listener")
	}
	unblock()
	finishCtx, finishCancel := context.WithTimeout(context.Background(), time.Second)
	defer finishCancel()
	if err := server.Shutdown(finishCtx); err != nil {
		t.Errorf("Shutdown after Accept returned: %v", err)
	}
	_ = clientConnection.SetReadDeadline(time.Now().Add(time.Second))
	var data [1]byte
	if _, err := clientConnection.Read(data[:]); !errors.Is(err, io.EOF) {
		t.Errorf("late accepted connection read error=%v, want EOF", err)
	}
}

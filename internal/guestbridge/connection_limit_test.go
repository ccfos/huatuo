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
	"errors"
	"net"
	"testing"
	"time"

	"github.com/ccfos/huatuo/internal/toolstream/transport"

	"github.com/prometheus/client_golang/prometheus"
)

func waitForConnectionLimitCondition(timeout time.Duration, condition func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return condition()
}

// TestConnectionLimiterRejectsOnlyNewConnections verifies the global connection limit:
// only new connections are closed at capacity; existing connections are unaffected, and their slots can be reused
// after they exit.
func TestConnectionLimiterRejectsOnlyNewConnections(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Errorf("listen: %v", err)
		return
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})

	limited := newConnectionLimiter(1, prometheus.NewGauge(prometheus.GaugeOpts{})).
		wrap(listener)
	accepted := make(chan net.Conn, 1)
	acceptErrors := make(chan error, 1)
	accept := func() {
		connection, err := limited.Accept()
		if err != nil {
			acceptErrors <- err
			return
		}
		accepted <- connection
	}

	go accept()
	firstClient, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Errorf("dial first connection: %v", err)
		return
	}
	t.Cleanup(func() {
		_ = firstClient.Close()
	})
	var firstServer net.Conn
	select {
	case firstServer = <-accepted:
	case err := <-acceptErrors:
		t.Errorf("accept first connection: %v", err)
		return
	case <-time.After(time.Second):
		t.Errorf("timed out accepting first connection")
		return
	}

	go accept()
	secondClient, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Errorf("dial second connection: %v", err)
		return
	}
	if err := secondClient.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Errorf("set second connection deadline: %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := secondClient.Read(buffer); err == nil {
		t.Errorf("connection above limit remained open")
	} else {
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			t.Errorf("connection above limit timed out instead of being rejected")
		}
	}
	_ = secondClient.Close()

	if err := firstServer.Close(); err != nil {
		t.Errorf("close first accepted connection: %v", err)
		return
	}
	thirdClient, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Errorf("dial third connection: %v", err)
		return
	}
	defer thirdClient.Close()
	select {
	case thirdServer := <-accepted:
		_ = thirdServer.Close()
	case err := <-acceptErrors:
		t.Errorf("accept third connection: %v", err)
	case <-time.After(time.Second):
		t.Errorf("released connection slot was not reusable")
	}
}

// TestHandshakeTimeoutReleasesConnectionSlot verifies that a connection that
// never sends its handshake cannot permanently prevent a valid guest from connecting.
func TestHandshakeTimeoutReleasesConnectionSlot(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	limiter := newConnectionLimiter(1, prometheus.NewGauge(prometheus.GaugeOpts{}))
	budget, err := transport.NewDecodeBudget(1024)
	if err != nil {
		t.Fatalf("NewDecodeBudget: %v", err)
	}
	received := make(chan struct{}, 1)
	server, err := transport.ServeWithOptions(limiter.wrap(listener), func(
		_ *transport.Session,
		chunk transport.ChunkMsg,
	) {
		if len(chunk.Data) > 0 {
			received <- struct{}{}
		}
	}, transport.ServerOptions{
		MaxMessageSize:   1024,
		HandshakeTimeout: 50 * time.Millisecond,
		DecodeBudget:     budget,
	})
	if err != nil {
		t.Fatalf("NewServerWithListener: %v", err)
	}
	t.Cleanup(func() {
		_ = server.Close()
	})

	idleClient, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial idle client: %v", err)
	}
	t.Cleanup(func() {
		_ = idleClient.Close()
	})
	if !waitForConnectionLimitCondition(time.Second, func() bool {
		return len(limiter.slots) == 1
	}) {
		t.Fatal("idle connection did not acquire the slot")
	}
	if !waitForConnectionLimitCondition(time.Second, func() bool {
		return len(limiter.slots) == 0
	}) {
		t.Fatal("handshake timeout did not release the slot")
	}

	validConnection, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial valid client: %v", err)
	}
	validClient, err := transport.NewClientWithConn(
		validConnection,
		transport.ClientMetadata{ToolName: "test", Version: "1.0", TaskID: ""},
	)
	if err != nil {
		t.Fatalf("NewClientWithConn: %v", err)
	}
	t.Cleanup(func() { _ = validClient.End() })
	if err := validClient.SendChunk([]byte(`{"status":"ok"}`), false); err != nil {
		t.Fatalf("Send: %v", err)
	}
	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("valid guest was not accepted after slot release")
	}
}

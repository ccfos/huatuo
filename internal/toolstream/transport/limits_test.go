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
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"
)

func TestClientHandshakeCancellation(t *testing.T) {
	client, peer := net.Pipe()
	defer peer.Close()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := NewClientWithConnContext(ctx, client, "guest-document", "1", "task", ProtocolVersion)
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("handshake error=%v", err)
		}
	case <-time.After(time.Second):
		t.Error("canceled handshake stayed blocked")
	}
}

func TestConnectionContextCancellationInterruptsIdleRead(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	ctx, cancel := context.WithCancel(t.Context())
	server := &Server{connContext: func(context.Context, net.Conn) (context.Context, error) { return ctx, nil }, handler: func(*Session, ChunkMsg) { t.Error("idle connection dispatched data") }}
	done := make(chan struct{})
	go func() { defer close(done); server.handleConn(t.Context(), peer) }()
	connection, err := NewClientWithConn(client, "guest-document", "1", "task", ProtocolVersion)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("idle receive did not stop on connection cancellation")
	}
}

func TestDecoderRejectsOversizedFrameBeforeBody(t *testing.T) {
	budget, err := NewDecodeBudget(64)
	if err != nil {
		t.Fatal(err)
	}
	for _, segmentID := range []uint32{0, maxStreamSegmentID + 1} {
		var header [8]byte
		binary.LittleEndian.PutUint32(header[:4], segmentID)
		binary.LittleEndian.PutUint32(header[4:], 1024)
		reader := &countingReader{reader: bytes.NewReader(header[:])}
		decoder := newMessageDecoder(reader, 64, budget)
		if msg, err := decoder.Decode(t.Context()); err == nil {
			msg.Release()
			t.Error("oversized header accepted")
		}
		if reader.read.Load() != 8 {
			t.Errorf("bytes read=%d", reader.read.Load())
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := budget.acquire(ctx, 64); err != nil {
		t.Errorf("rejected headers leaked budget: %v", err)
	} else {
		budget.release(64)
	}
}

func TestServerHandshakeTimeout(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	reported := make(chan error, 1)
	server := &Server{handshakeTimeout: 30 * time.Millisecond, onReceiveError: func(err error) { reported <- err }}
	done := make(chan struct{})
	go func() { defer close(done); server.handleConn(t.Context(), peer) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handshake did not time out")
	}
	select {
	case err := <-reported:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Errorf("error=%v", err)
		}
	default:
		t.Error("timeout not reported")
	}
}

func TestFrameTimeoutWithoutSharedBudget(t *testing.T) {
	client, peer := net.Pipe()
	defer client.Close()
	defer peer.Close()
	decoder := newMessageDecoder(peer, 1024, nil)
	decoder.frameReadTimeout = 30 * time.Millisecond
	done := make(chan error, 1)
	go func() { msg, err := decoder.Decode(t.Context()); msg.Release(); done <- err }()
	if _, err := client.Write([]byte{0}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var timeout net.Error
		if !errors.As(err, &timeout) || !timeout.Timeout() {
			t.Errorf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Error("partial frame did not time out")
	}
}

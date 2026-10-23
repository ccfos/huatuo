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
	"net"
	"testing"
	"time"
)

// TestServerConnectionContext verifies that connection metadata is bound before the handshake:
// valid handlers read the Host-provided context independently of Guest TaskID; identity resolution failures
// prevent handshake reads and data dispatch.
func TestServerConnectionContext(t *testing.T) {
	for _, reject := range []bool{false, true} {
		name := "trusted connection"
		if reject {
			name = "rejected connection"
		}
		t.Run(name, func(t *testing.T) {
			type identityKey struct{}
			clientConn, serverConn := net.Pipe()
			defer clientConn.Close()
			defer serverConn.Close()
			_ = clientConn.SetDeadline(time.Now().Add(2 * time.Second))
			received := make(chan string, 1)
			rejected := errors.New("unknown sandbox")
			receiveErrors := make(chan error, 1)
			srv := &Server{
				connContext: func(ctx context.Context, conn net.Conn) (context.Context, error) {
					if conn.RemoteAddr().Network() != "pipe" {
						t.Errorf("unexpected peer: %v", conn.RemoteAddr())
					}
					if reject {
						return nil, rejected
					}
					return context.WithValue(ctx, identityKey{}, "trusted-sandbox"), nil
				},
				onReceiveError: func(err error) { receiveErrors <- err },
				handler: func(session *Session, chunk ChunkMsg) {
					if len(chunk.Data) > 0 {
						identity, _ := session.Context.Value(identityKey{}).(string)
						received <- identity
					}
				},
			}
			done := make(chan struct{})
			go func() { defer close(done); defer serverConn.Close(); srv.handleConn(t.Context(), serverConn) }()
			client, err := NewClientWithConn(
				clientConn,
				ClientMetadata{
					ServiceName: "guest-document",
					ToolName:    "guest-document",
					Version:     "2.2.0",
					TaskID:      "forged-sandbox",
				},
			)
			if reject {
				if err == nil {
					t.Errorf("rejected connection completed handshake")
				}
				select {
				case got := <-receiveErrors:
					if !errors.Is(got, rejected) {
						t.Errorf("error=%v", got)
					}
				case <-time.After(time.Second):
					t.Errorf("rejection not reported")
				}
			} else {
				if err != nil {
					t.Errorf("handshake: %v", err)
					return
				}
				if err := client.Send(t.Context(), []byte("document"), SendOptions{}); err != nil {
					t.Errorf("SendChunk: %v", err)
					return
				}
				select {
				case identity := <-received:
					if identity != "trusted-sandbox" {
						t.Errorf("identity=%q", identity)
					}
				case <-time.After(time.Second):
					t.Errorf("document not delivered")
				}
			}
			_ = clientConn.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Errorf("handler did not exit")
			}
		})
	}
}

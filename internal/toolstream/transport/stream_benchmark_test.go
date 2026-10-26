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
	"net"
	"testing"

	capnp "capnproto.org/go/capnp/v3"
)

func BenchmarkServerChunkStream(b *testing.B) {
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	done := make(chan struct{})
	server := &Server{handler: func(_ *Session, _ ChunkMsg) {}}
	go func() {
		defer close(done)
		defer serverConn.Close()
		server.handleConn(b.Context(), serverConn)
	}()

	client := &Client{encoder: capnp.NewEncoder(clientConn), conn: clientConn}
	if err := client.handshake("profiler", "profiler", "1", "benchmark"); err != nil {
		b.Fatal(err)
	}
	data := make([]byte, 256<<10)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := client.Send(b.Context(), data, SendOptions{}); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	if err := client.Send(b.Context(), nil, SendOptions{End: true}); err != nil {
		b.Fatal(err)
	}
	<-done
}

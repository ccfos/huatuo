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
	"io"
	"net"
	"testing"
	"time"
)

func TestEncodeChunkFrameRoundTrip(t *testing.T) {
	payload := []byte(`{"tracer_id":"guest-observation"}`)
	frame, err := EncodeChunkFrame(payload)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoder := newMessageDecoder(bytes.NewReader(frame), uint64(len(frame)+1024), nil)
	message, err := decoder.Decode(context.Background())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	defer message.Release()

	chunk, err := parseChunk(message.message)
	if err != nil {
		t.Fatalf("parse chunk: %v", err)
	}
	if !bytes.Equal(chunk.Data, payload) {
		t.Fatalf("payload = %q, want %q", chunk.Data, payload)
	}
	if chunk.Flush || chunk.End || chunk.Err != "" {
		t.Fatalf("unexpected flags: flush=%t end=%t err=%q", chunk.Flush, chunk.End, chunk.Err)
	}
}

func TestSendFrameWritesPartialWrites(t *testing.T) {
	frame, err := EncodeChunkFrame(bytes.Repeat([]byte("payload"), 32))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	conn := &shortWriteConn{limit: 7}
	client := &Client{conn: conn}
	if err := client.SendFrame(frame); err != nil {
		t.Fatalf("send frame: %v", err)
	}
	if !bytes.Equal(conn.Bytes(), frame) {
		t.Fatalf("wrote %d bytes, want %d", conn.Len(), len(frame))
	}
}

func BenchmarkEncodeChunkFrame(b *testing.B) {
	payload := bytes.Repeat([]byte{'x'}, 64<<10)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := EncodeChunkFrame(payload); err != nil {
			b.Fatal(err)
		}
	}
}

// shortWriteConn accepts at most limit bytes per Write to exercise SendFrame's
// partial-write loop without a real socket.
type shortWriteConn struct {
	bytes.Buffer
	limit int
}

func (c *shortWriteConn) Write(p []byte) (int, error) {
	if len(p) > c.limit {
		p = p[:c.limit]
	}
	return c.Buffer.Write(p)
}

func (*shortWriteConn) Read([]byte) (int, error)         { return 0, io.EOF }
func (*shortWriteConn) Close() error                     { return nil }
func (*shortWriteConn) LocalAddr() net.Addr              { return nil }
func (*shortWriteConn) RemoteAddr() net.Addr             { return nil }
func (*shortWriteConn) SetDeadline(time.Time) error      { return nil }
func (*shortWriteConn) SetReadDeadline(time.Time) error  { return nil }
func (*shortWriteConn) SetWriteDeadline(time.Time) error { return nil }

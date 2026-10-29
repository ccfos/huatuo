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
	"fmt"
	"io"
	"math"
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
	if chunk.Commit || chunk.End || chunk.Err != "" {
		t.Fatalf("unexpected flags: commit=%t end=%t err=%q", chunk.Commit, chunk.End, chunk.Err)
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

// TestChunkFrameSize 验证空载荷、字对齐边界和大载荷的预估值与编码一致，
// 并拒绝负数和整数溢出的输入。
func TestChunkFrameSize(t *testing.T) {
	for _, size := range []int{0, 1, 7, 8, 9, 127, 128, 64 << 10, 8 << 20, -1, math.MaxInt} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			estimated, err := ChunkFrameSize(size)
			if size < 0 || size == math.MaxInt {
				if err == nil {
					t.Fatal("invalid payload size accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			frame, err := EncodeChunkFrame(make([]byte, size))
			if err != nil {
				t.Fatal(err)
			}
			if len(frame) != estimated {
				t.Fatalf("encoded=%d, reserved=%d", len(frame), estimated)
			}
		})
	}
}

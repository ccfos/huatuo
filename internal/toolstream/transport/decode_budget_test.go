// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
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
	"sync/atomic"
	"testing"
	"time"

	capnp "capnproto.org/go/capnp/v3"
)

type countingReader struct {
	reader *bytes.Reader
	read   atomic.Int64
}

func (reader *countingReader) Read(buffer []byte) (int, error) {
	n, err := reader.reader.Read(buffer)
	reader.read.Add(int64(n))
	return n, err
}

func marshalChunk(t *testing.T, payload []byte) []byte {
	t.Helper()

	message, segment, err := capnp.NewMessage(capnp.SingleSegment(nil))
	if err != nil {
		t.Fatalf("new message: %v", err)
	}
	defer message.Release()
	root, err := NewRootMessage(segment)
	if err != nil {
		t.Fatalf("new root: %v", err)
	}
	chunk, err := root.NewChunk()
	if err != nil {
		t.Fatalf("new chunk: %v", err)
	}
	if err := chunk.SetData(payload); err != nil {
		t.Fatalf("set data: %v", err)
	}
	frame, err := message.Marshal()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return frame
}

// TestBudgetedDecoderAcquiresBeforeReadingBody verifies that a decoder only
// reads the bounded frame header while the shared byte budget is exhausted.
func TestBudgetedDecoderAcquiresBeforeReadingBody(t *testing.T) {
	frame := marshalChunk(t, bytes.Repeat([]byte{'x'}, 256))
	budget, err := NewDecodeBudget(uint64(len(frame)))
	if err != nil {
		t.Fatalf("NewDecodeBudget: %v", err)
	}
	if err := budget.acquire(t.Context(), uint64(len(frame))); err != nil {
		t.Fatalf("reserve budget: %v", err)
	}

	reader := &countingReader{reader: bytes.NewReader(frame)}
	decoder := newMessageDecoder(reader, uint64(len(frame)), budget)
	type decodeResult struct {
		message *decodedMessage
		err     error
	}
	result := make(chan decodeResult, 1)
	go func() {
		message, err := decoder.Decode(t.Context())
		result <- decodeResult{message: message, err: err}
	}()

	deadline := time.Now().Add(time.Second)
	for reader.read.Load() < 8 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := reader.read.Load(); got != 8 {
		t.Fatalf("bytes read while budget exhausted=%d, want header size 8", got)
	}
	select {
	case decoded := <-result:
		if decoded.message != nil {
			decoded.message.Release()
		}
		t.Fatalf("Decode completed while budget exhausted: %v", decoded.err)
	default:
	}

	budget.release(uint64(len(frame)))
	decoded := <-result
	if decoded.err != nil {
		t.Fatalf("Decode: %v", decoded.err)
	}

	blockedContext, cancelBlocked := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancelBlocked()
	if err := budget.acquire(blockedContext, uint64(len(frame))); err == nil {
		budget.release(uint64(len(frame)))
		t.Fatal("frame budget was released before Message.Release")
	}

	decoded.message.Release()
	availableContext, cancelAvailable := context.WithTimeout(t.Context(), time.Second)
	defer cancelAvailable()
	if err := budget.acquire(availableContext, uint64(len(frame))); err != nil {
		t.Fatalf("frame budget was not released: %v", err)
	}
	budget.release(uint64(len(frame)))
}

// TestBudgetedDecoderReleasesAfterTruncatedFrame verifies that an incomplete
// body does not leak its shared byte reservation.
func TestBudgetedDecoderReleasesAfterTruncatedFrame(t *testing.T) {
	frame := marshalChunk(t, bytes.Repeat([]byte{'x'}, 256))
	budget, err := NewDecodeBudget(uint64(len(frame)))
	if err != nil {
		t.Fatalf("NewDecodeBudget: %v", err)
	}
	decoder := newMessageDecoder(
		bytes.NewReader(frame[:len(frame)-1]),
		uint64(len(frame)),
		budget,
	)
	if message, err := decoder.Decode(t.Context()); err == nil {
		message.Release()
		t.Fatal("Decode truncated frame error=nil")
	}

	availableContext, cancelAvailable := context.WithTimeout(t.Context(), time.Second)
	defer cancelAvailable()
	if err := budget.acquire(availableContext, uint64(len(frame))); err != nil {
		t.Fatalf("truncated frame leaked decode budget: %v", err)
	}
	budget.release(uint64(len(frame)))
}

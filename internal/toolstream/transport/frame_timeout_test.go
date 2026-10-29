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

// TestBudgetedDecoderFrameTimeout verifies that a started frame must finish before its deadline:
// incomplete headers, missing bodies after reserving the full budget, and waits for shared budget all time out;
// normal connections can still decode afterward, confirming that no budget leaks.
func TestBudgetedDecoderFrameTimeout(t *testing.T) {
	frame := marshalChunk(t, []byte("kernel_sched_tick"))
	for _, tc := range []struct {
		name    string
		prefix  int
		reserve bool
	}{
		{name: "partial-header", prefix: 1},
		{name: "stalled-body", prefix: capnpWordSize},
		{name: "waiting-for-budget", prefix: capnpWordSize, reserve: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			budget, err := NewDecodeBudget(uint64(len(frame)))
			if err != nil {
				t.Errorf("NewDecodeBudget: %v", err)
				return
			}
			if tc.reserve {
				if err := budget.acquire(t.Context(), uint64(len(frame))); err != nil {
					t.Errorf("reserve budget: %v", err)
					return
				}
			}
			client, connection := net.Pipe()
			defer client.Close()
			defer connection.Close()
			decoder := newMessageDecoder(connection, uint64(len(frame)), budget)
			decoder.frameReadTimeout = 50 * time.Millisecond
			result := make(chan error, 1)
			go func() {
				message, err := decoder.Decode(t.Context())
				message.Release()
				result <- err
			}()
			if err := client.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Errorf("SetWriteDeadline: %v", err)
				return
			}
			if _, err := client.Write(frame[:tc.prefix]); err != nil {
				t.Errorf("write frame prefix: %v", err)
				return
			}
			select {
			case err := <-result:
				var timeout net.Error
				if !errors.Is(err, context.DeadlineExceeded) && (!errors.As(err, &timeout) || !timeout.Timeout()) {
					t.Errorf("Decode error = %v, want frame timeout", err)
				}
			case <-time.After(2 * time.Second):
				t.Errorf("incomplete frame did not time out")
				return
			}
			if tc.reserve {
				budget.release(uint64(len(frame)))
			}

			// A second connection needs the entire budget to decode successfully.
			healthyClient, healthyConnection := net.Pipe()
			defer healthyClient.Close()
			defer healthyConnection.Close()
			if err := healthyConnection.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
				t.Errorf("SetReadDeadline: %v", err)
				return
			}
			written := make(chan error, 1)
			go func() {
				_, err := healthyClient.Write(frame)
				written <- err
			}()
			healthyDecoder := newMessageDecoder(healthyConnection, uint64(len(frame)), budget)
			healthyDecoder.frameReadTimeout = 100 * time.Millisecond
			message, err := healthyDecoder.Decode(t.Context())
			if err != nil {
				t.Errorf("healthy connection could not reuse budget: %v", err)
				return
			}
			defer message.Release()
			chunk, err := parseChunk(message.message)
			if err != nil || string(chunk.Data) != "kernel_sched_tick" {
				t.Errorf("decoded chunk = %q, error = %v", chunk.Data, err)
			}
			if err := <-written; err != nil {
				t.Errorf("write healthy frame: %v", err)
			}
		})
	}
}

// TestBudgetedDecoderIdleConnection verifies that waiting for the first or next frame does not consume the receive
// timeout:
// a connection idle beyond the frame timeout can still receive a full frame, and the previous frame's read
// deadline does not affect the next frame.
func TestBudgetedDecoderIdleConnection(t *testing.T) {
	frame := marshalChunk(t, []byte("kernel_sched_tick"))
	budget, err := NewDecodeBudget(uint64(len(frame)))
	if err != nil {
		t.Errorf("NewDecodeBudget: %v", err)
		return
	}
	client, connection := net.Pipe()
	defer client.Close()
	defer connection.Close()
	decoder := newMessageDecoder(connection, uint64(len(frame)), budget)
	decoder.frameReadTimeout = 50 * time.Millisecond
	for _, phase := range []string{"before-first-frame", "between-frames"} {
		result := make(chan error, 1)
		go func() {
			message, err := decoder.Decode(t.Context())
			message.Release()
			result <- err
		}()
		select {
		case err := <-result:
			t.Errorf("%s: idle connection completed Decode: %v", phase, err)
			return
		case <-time.After(2 * decoder.frameReadTimeout):
		}
		if err := client.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Errorf("SetWriteDeadline: %v", err)
			return
		}
		if _, err := client.Write(frame); err != nil {
			t.Errorf("%s: write frame: %v", phase, err)
			return
		}
		select {
		case err := <-result:
			if err != nil {
				t.Errorf("%s: Decode: %v", phase, err)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("%s: complete frame did not decode", phase)
			return
		}
	}
}

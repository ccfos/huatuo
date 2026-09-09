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

package exec

import (
	"bytes"
	"strings"
	"testing"
)

func TestOutputBufferKeepsOldestBytesAndReportsOverflow(t *testing.T) {
	buffer := newOutputBuffer(4)
	if n, err := buffer.Write([]byte("12345")); err != nil || n != 5 {
		t.Fatalf("Write() = (%d, %v), want (5, nil)", n, err)
	}
	if got := string(buffer.Bytes()); got != "1234" {
		t.Errorf("Bytes() = %q, want 1234", got)
	}
	if !buffer.Exceeded() {
		t.Error("Exceeded() = false, want true")
	}

	output := buffer.Bytes()
	output[0] = 'x'
	if got := string(buffer.Bytes()); got != "1234" {
		t.Errorf("Bytes() after caller mutation = %q, want 1234", got)
	}
}

func TestTailBufferKeepsNewestBytes(t *testing.T) {
	var buffer tailBuffer
	first := strings.Repeat("a", maxErrorOutputBytes-2)
	if _, err := buffer.Write([]byte(first)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if _, err := buffer.Write([]byte("bcde")); err != nil {
		t.Fatalf("Write() overflow error = %v", err)
	}
	if _, err := buffer.Write([]byte("fgh")); err != nil {
		t.Fatalf("Write() second overflow error = %v", err)
	}
	want := first[5:] + "bcdefgh"
	if got := string(buffer.Bytes()); got != want {
		t.Errorf("Bytes() length = %d, want %d; suffix = %q", len(got), len(want), got[len(got)-8:])
	}

	oversized := strings.Repeat("x", maxErrorOutputBytes) + "tail"
	if _, err := buffer.Write([]byte(oversized)); err != nil {
		t.Fatalf("Write() oversized error = %v", err)
	}
	want = oversized[len(oversized)-maxErrorOutputBytes:]
	if got := string(buffer.Bytes()); got != want {
		t.Errorf("Bytes() after oversized write length = %d, want %d", len(got), len(want))
	}

	output := buffer.Bytes()
	output[0] = 'z'
	if got := string(buffer.Bytes()); got != want {
		t.Error("Bytes() exposed the retained error buffer")
	}
}

func BenchmarkTailBufferWriteFull(b *testing.B) {
	var buffer tailBuffer
	_, _ = buffer.Write(bytes.Repeat([]byte{'a'}, maxErrorOutputBytes))
	data := bytes.Repeat([]byte{'b'}, 64)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = buffer.Write(data)
	}
}

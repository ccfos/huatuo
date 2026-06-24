// Copyright 2025 The HuaTuo Authors
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

package bpf_test

import (
	"bytes"
	"encoding/binary"
	"testing"
	"unsafe"

	"huatuo-bamai/internal/bpf"
)

func TestBpfDbgEventSize(t *testing.T) {
	var e bpf.BpfDbgEvent
	// struct bpf_dbg_event: u64(8) + u32(4) + u32(4) + u64[4](32) + char[64](64) = 112
	if s := unsafe.Sizeof(e); s != 112 {
		t.Fatalf("BpfDbgEvent size mismatch: got %d, expected 112", s)
	}
}

func TestBpfDbgEventRoundTrip(t *testing.T) {
	e := bpf.BpfDbgEvent{
		Timestamp: 123456789,
		FileID:    0x0001,
		Line:      42,
		Args:      [4]uint64{1, 2, 3, 4},
	}
	copy(e.Msg[:], "hello world\x00")

	var buf bytes.Buffer
	if err := binary.Write(&buf, binary.NativeEndian, e); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 112 {
		t.Fatalf("serialized size: got %d, expected 112", buf.Len())
	}

	var decoded bpf.BpfDbgEvent
	if err := binary.Read(bytes.NewReader(buf.Bytes()), binary.NativeEndian, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Timestamp != e.Timestamp || decoded.FileID != e.FileID ||
		decoded.Line != e.Line || decoded.Args != e.Args {
		t.Fatalf("round-trip mismatch: %+v != %+v", decoded, e)
	}
	if nullTerminated(decoded.Msg[:]) != "hello world" {
		t.Fatalf("msg mismatch: %q", string(decoded.Msg[:]))
	}
}

func nullTerminated(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

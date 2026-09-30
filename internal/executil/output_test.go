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

package executil

import (
	"bytes"
	"strings"
	"testing"
)

func TestOutputBufferKeepsOldestBytesAndReportsOverflow(t *testing.T) {
	buffer := outputBuffer{limit: 4}
	if n, err := buffer.Write([]byte("12345")); err != nil || n != 5 {
		t.Fatalf("Write() = (%d, %v), want (5, nil)", n, err)
	}

	if data, _ := buffer.Snapshot(); string(data) != "1234" {
		t.Errorf("Snapshot() = %q, want 1234", data)
	}

	if _, exceeded := buffer.Snapshot(); !exceeded {
		t.Error("Snapshot() overflow = false, want true")
	}

	output, _ := buffer.Snapshot()
	output[0] = 'x'
	if data, _ := buffer.Snapshot(); string(data) != "1234" {
		t.Errorf("Snapshot() after caller mutation = %q, want 1234", data)
	}
}

func TestOutputBufferLimitBoundaries(t *testing.T) {
	tests := []struct {
		name         string
		limit        int
		writes       []string
		want         string
		wantExceeded bool
	}{
		{
			name:   "empty writes",
			limit:  4,
			writes: []string{"", ""},
		},
		{
			name:   "empty write at limit",
			limit:  4,
			writes: []string{"12", "34", ""},
			want:   "1234",
		},
		{
			name:         "cross limit",
			limit:        4,
			writes:       []string{"123", "45"},
			want:         "1234",
			wantExceeded: true,
		},
		{
			name:         "write after limit",
			limit:        4,
			writes:       []string{"1234", "", "5", ""},
			want:         "1234",
			wantExceeded: true,
		},
		{
			name:   "empty zero capacity buffer",
			writes: []string{""},
		},
		{
			name:         "write to zero capacity buffer",
			writes:       []string{"1", ""},
			wantExceeded: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			buffer := outputBuffer{limit: test.limit}
			for _, data := range test.writes {
				if n, err := buffer.Write([]byte(data)); err != nil || n != len(data) {
					t.Fatalf(
						"Write() = (%d, %v), want (%d, nil)",
						n,
						err,
						len(data),
					)
				}
			}

			data, exceeded := buffer.Snapshot()
			if string(data) != test.want {
				t.Errorf("Snapshot() data = %q, want %q", data, test.want)
			}

			if exceeded != test.wantExceeded {
				t.Errorf("Snapshot() overflow = %t, want %t", exceeded, test.wantExceeded)
			}
		})
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
		t.Errorf(
			"Bytes() length = %d, want %d; suffix = %q",
			len(got),
			len(want),
			got[len(got)-8:],
		)
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

func TestTailBufferWriteWrapsAtEnd(t *testing.T) {
	var buffer tailBuffer
	writes := []string{
		strings.Repeat("a", maxErrorOutputBytes),
		strings.Repeat("b", maxErrorOutputBytes-1),
		"cd",
	}
	for _, data := range writes {
		if n, err := buffer.Write([]byte(data)); err != nil || n != len(data) {
			t.Fatalf(
				"Write() = (%d, %v), want (%d, nil)",
				n,
				err,
				len(data),
			)
		}
	}

	want := strings.Repeat("b", maxErrorOutputBytes-2) + "cd"
	if got := string(buffer.Bytes()); got != want {
		t.Errorf("Bytes() after wrapped write must contain %d b bytes followed by cd", maxErrorOutputBytes-2)
	}
}

func BenchmarkOutputBufferWriteFull(b *testing.B) {
	buffer := outputBuffer{limit: defaultMaxOutputBytes}
	_, _ = buffer.Write(bytes.Repeat([]byte{'a'}, buffer.limit))
	data := bytes.Repeat([]byte{'b'}, 64)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = buffer.Write(data)
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

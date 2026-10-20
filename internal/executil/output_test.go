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
	"sync"
	"testing"
)

func TestOutputBufferKeepsNewestBytes(t *testing.T) {
	tests := []struct {
		name     string
		maxBytes int
		writes   []string
	}{
		{"empty", 4, nil},
		{"empty writes", 4, []string{"", ""}},
		{"below limit", 4, []string{"1", "23"}},
		{"exact limit", 4, []string{"12", "34", ""}},
		{"cross limit", 4, []string{"123", "45"}},
		{"repeated overflow", 4, []string{"1234", "", "5", "67", "89", ""}},
		{"oversized write", 4, []string{"12", "3456789", "ab"}},
		{"replace full buffer", 4, []string{"1234", "5678"}},
		{"single byte", 1, []string{"1", "23", "4", ""}},
		{"zero capacity", 0, []string{"", "123", "4"}},
		{"many writes", 64, strings.Split(strings.Repeat("0123456789", 100), "")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buffer := outputBuffer{maxBytes: tt.maxBytes}
			var written string
			for _, data := range tt.writes {
				if n, err := buffer.Write([]byte(data)); err != nil || n != len(data) {
					t.Fatalf("Write() = (%d, %v), want (%d, nil)", n, err, len(data))
				}
				written += data
				want := written[max(0, len(written)-tt.maxBytes):]
				if got := string(buffer.Bytes()); got != want {
					t.Fatalf("Bytes() = %q, want %q", got, want)
				}
			}
			if len(buffer.Bytes()) > tt.maxBytes {
				t.Fatal("retained output exceeds capacity")
			}
		})
	}
}

func TestOutputBufferBytesOwnsSnapshot(t *testing.T) {
	buffer := outputBuffer{maxBytes: 4}
	input := []byte("1234")
	_, _ = buffer.Write(input)
	input[0] = 'x'
	snapshot := buffer.Bytes()
	if string(snapshot) != "1234" {
		t.Fatalf("Bytes() retained caller's input: %q", snapshot)
	}
	snapshot[0] = 'x'
	if got := string(buffer.Bytes()); got != "1234" {
		t.Fatalf("Bytes() exposed buffer storage: %q", got)
	}
	_, _ = buffer.Write([]byte("56789"))
	if string(snapshot) != "x234" {
		t.Fatalf("Write() modified a previous snapshot: %q", snapshot)
	}
}

func TestOutputBufferConcurrentWritesAndSnapshots(t *testing.T) {
	buffer := outputBuffer{maxBytes: 64}
	var workers sync.WaitGroup
	start := make(chan struct{})
	for range 4 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for range 1000 {
				if n, err := buffer.Write([]byte("01234567")); n != 8 || err != nil {
					t.Errorf("Write() = (%d, %v), want (8, nil)", n, err)
					return
				}
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		for range 1000 {
			data := buffer.Bytes()
			if len(data) > 64 || len(data)%8 != 0 || string(data) != strings.Repeat("01234567", len(data)/8) {
				t.Errorf("inconsistent snapshot: %q", data)
				return
			}
			if len(data) > 0 {
				data[0] = 'x'
			}
		}
	}()
	close(start)
	workers.Wait()
	if got := string(buffer.Bytes()); got != strings.Repeat("01234567", 8) {
		t.Fatalf("final snapshot = %q", got)
	}
}

func BenchmarkOutputBufferWriteFull(b *testing.B) {
	buffer := outputBuffer{maxBytes: defaultMaxOutputBytes}
	_, _ = buffer.Write(bytes.Repeat([]byte{'a'}, buffer.maxBytes))
	data := bytes.Repeat([]byte{'b'}, 64)

	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = buffer.Write(data)
	}
}

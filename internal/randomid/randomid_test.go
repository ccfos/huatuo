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

package randomid

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	seen := make(map[string]struct{})
	for range 1000 {
		id, err := New()
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if len(id) != length {
			t.Fatalf("New() length = %d, want %d", len(id), length)
		}
		if strings.IndexFunc(id, func(r rune) bool {
			return !strings.ContainsRune(charset, r)
		}) >= 0 {
			t.Fatalf("New() = %q contains characters outside the charset", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != 1000 {
		t.Fatalf("New() produced %d unique IDs out of 1000", len(seen))
	}
}

// seqReader serves queued bytes; a nil entry yields a read error.
type seqReader struct {
	batches [][]byte
}

func (s *seqReader) Read(p []byte) (int, error) {
	for len(s.batches) > 0 {
		b := s.batches[0]
		s.batches = s.batches[1:]
		if b == nil {
			return 0, errors.New("reader failure")
		}
		if len(b) <= len(p) {
			copy(p, b)
			return len(b), nil
		}
		copy(p, b[:len(p)])
		s.batches = append([][]byte{b[len(p):]}, s.batches...)
		return len(p), nil
	}
	return 0, errors.New("unexpected read")
}

func TestGenerateRejectionAndRefill(t *testing.T) {
	// Reject every byte of the first batch (>= maxByte), then supply
	// accepted bytes; generate must refill and still produce a full ID.
	rejected := bytes.Repeat([]byte{0xff}, length)
	accepted := make([]byte, length)
	for i := range accepted {
		accepted[i] = byte(i % maxByte)
	}
	id, err := generate(&seqReader{batches: [][]byte{rejected, accepted}})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(id) != length {
		t.Fatalf("generate() length = %d, want %d", len(id), length)
	}
	for i, c := range id {
		if charset[int(accepted[i])%len(charset)] != byte(c) {
			t.Fatalf("generate()[%d] = %q, want %q", i, c,
				charset[int(accepted[i])%len(charset)])
		}
	}
}

func TestGenerateReaderErrorPropagates(t *testing.T) {
	_, err := generate(&seqReader{batches: [][]byte{nil}})
	if err == nil || !strings.Contains(err.Error(), "generate random ID") {
		t.Fatalf("generate() error = %v, want wrapped reader failure", err)
	}
}

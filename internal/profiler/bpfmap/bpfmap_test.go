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

package bpfmap

import (
	"encoding/binary"
	"strings"
	"testing"

	"huatuo-bamai/internal/bpf"
)

type readMapBPF struct {
	bpf.BPF
	value []byte
}

func (b *readMapBPF) ReadMap(_ uint32, _ []byte) ([]byte, error) {
	return b.value, nil
}

func TestReadUint64RejectsMalformedValues(t *testing.T) {
	var valid [8]byte
	binary.LittleEndian.PutUint64(valid[:], 42)

	tests := []struct {
		name    string
		value   []byte
		want    uint64
		wantErr string
	}{
		{name: "valid", value: valid[:], want: 42},
		{name: "empty", wantErr: "has 0 bytes, want 8"},
		{name: "truncated", value: valid[:7], wantErr: "has 7 bytes, want 8"},
		{name: "oversized", value: append(valid[:], 0), wantErr: "has 9 bytes, want 8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ReadUint64(&readMapBPF{value: tt.value}, 7, 3)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ReadUint64() error = %v, want %q", err, tt.wantErr)
				}
				if !strings.Contains(err.Error(), "map 7 index 3") {
					t.Errorf("ReadUint64() error = %q, want map and index context", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadUint64() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("ReadUint64() = %d, want %d", got, tt.want)
			}
		})
	}
}

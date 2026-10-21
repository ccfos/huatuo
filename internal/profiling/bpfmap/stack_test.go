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
	"errors"
	"fmt"
	"testing"

	"github.com/cilium/ebpf"

	"github.com/ccfos/huatuo/internal/bpf"
)

type stackMapStub struct {
	bpf.BPF
	raw     []byte
	err     error
	mapID   uint32
	stackID uint32
}

func (s *stackMapStub) ReadMap(mapID uint32, key []byte) ([]byte, error) {
	s.mapID = mapID
	s.stackID = binary.LittleEndian.Uint32(key)
	return s.raw, s.err
}

func TestReadStackTrace(t *testing.T) {
	t.Parallel()
	raw := make([]byte, StackTraceLen*8)
	var want [StackTraceLen]uint64
	for i := range want {
		want[i] = uint64(i+1)<<32 | uint64(i)
		binary.LittleEndian.PutUint64(raw[i*8:], want[i])
	}
	readErr := errors.New("map read denied")
	for _, tt := range []struct {
		name    string
		id      uint32
		raw     []byte
		err     error
		wantErr error
	}{
		{
			name: "zero ID",
			raw:  raw,
		},
		{
			name: "positive ID",
			id:   42,
			raw:  raw,
		},
		{
			name:    "missing ID",
			err:     fmt.Errorf("lookup: %w", ebpf.ErrKeyNotExist),
			wantErr: ebpf.ErrKeyNotExist,
		},
		{
			name:    "backend failure",
			err:     readErr,
			wantErr: readErr,
		},
		{
			name:    "empty value",
			wantErr: ErrInvalidStackData,
		},
		{
			name:    "short value",
			raw:     raw[:len(raw)-1],
			wantErr: ErrInvalidStackData,
		},
		{
			name:    "oversized value",
			raw:     make([]byte, len(raw)+8),
			wantErr: ErrInvalidStackData,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			obj := &stackMapStub{
				raw: tt.raw,
				err: tt.err,
			}
			got, err := ReadStackTrace(obj, 7, tt.id)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if obj.mapID != 7 || obj.stackID != tt.id {
				t.Fatalf("lookup = (%d, %d), want (7, %d)", obj.mapID, obj.stackID, tt.id)
			}
			if tt.wantErr != nil {
				if got != ([StackTraceLen]uint64{}) {
					t.Fatal("failed read returned partial stack")
				}
				return
			}
			if got != want {
				t.Fatalf("stack = %v, want %v", got, want)
			}
			// Returned frames must not alias a backend buffer reused on the next read.
			got[0] = 0
			if binary.LittleEndian.Uint64(raw) != want[0] {
				t.Fatal("stack aliases backend data")
			}
		})
	}
}

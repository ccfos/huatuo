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

//go:build !didi

package bpf

import (
	"errors"
	"testing"
)

func TestDefaultBPFMapOperationsRejectUnknownMap(t *testing.T) {
	b := &defaultBPF{
		mapSpecs:    make(map[uint32]mapSpec),
		mapName2IDs: make(map[string]uint32),
	}

	tests := []struct {
		name string
		run  func(*testing.T) error
		want string
	}{
		{
			name: "EventPipe",
			run: func(t *testing.T) error {
				_, err := b.EventPipe(t.Context(), 42, 4096)
				return err
			},
			want: "bpf: map not found: ID 42",
		},
		{
			name: "EventPipeByName",
			run: func(t *testing.T) error {
				_, err := b.EventPipeByName(t.Context(), "missing", 4096)
				return err
			},
			want: `bpf: map not found: name "missing"`,
		},
		{
			name: "ReadMap",
			run: func(*testing.T) error {
				_, err := b.ReadMap(42, nil)
				return err
			},
			want: "bpf: map not found: ID 42",
		},
		{
			name: "WriteMapItems",
			run: func(*testing.T) error {
				return b.WriteMapItems(42, nil)
			},
			want: "bpf: map not found: ID 42",
		},
		{
			name: "DeleteMapItems",
			run: func(*testing.T) error {
				return b.DeleteMapItems(42, nil)
			},
			want: "bpf: map not found: ID 42",
		},
		{
			name: "DumpMap",
			run: func(*testing.T) error {
				_, err := b.DumpMap(42)
				return err
			},
			want: "bpf: map not found: ID 42",
		},
		{
			name: "DumpMapByName",
			run: func(*testing.T) error {
				_, err := b.DumpMapByName("missing")
				return err
			},
			want: `bpf: map not found: name "missing"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run(t)
			if !errors.Is(err, ErrMapNotFound) {
				t.Fatalf("map operation error=%v, want errors.Is ErrMapNotFound", err)
			}
			if err.Error() != tt.want {
				t.Errorf("map operation error=%q, want %q", err, tt.want)
			}
		})
	}
}

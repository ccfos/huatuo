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
	"context"
	"strings"
	"testing"
)

func TestDefaultBPFMapOperationsRejectUnknownMap(t *testing.T) {
	b := &defaultBPF{
		mapSpecs:    make(map[uint32]mapSpec),
		mapName2IDs: make(map[string]uint32),
	}

	tests := []struct {
		name string
		run  func() error
		want string
	}{
		{
			name: "EventPipe",
			run: func() error {
				_, err := b.EventPipe(context.Background(), 42, 4096)
				return err
			},
			want: "map 42 not found",
		},
		{
			name: "EventPipeByName",
			run: func() error {
				_, err := b.EventPipeByName(context.Background(), "missing", 4096)
				return err
			},
			want: `map "missing" not found`,
		},
		{
			name: "ReadMap",
			run: func() error {
				_, err := b.ReadMap(42, nil)
				return err
			},
			want: "map 42 not found",
		},
		{
			name: "WriteMapItems",
			run: func() error {
				return b.WriteMapItems(42, nil)
			},
			want: "map 42 not found",
		},
		{
			name: "DeleteMapItems",
			run: func() error {
				return b.DeleteMapItems(42, nil)
			},
			want: "map 42 not found",
		},
		{
			name: "DumpMap",
			run: func() error {
				_, err := b.DumpMap(42)
				return err
			},
			want: "map 42 not found",
		},
		{
			name: "DumpMapByName",
			run: func() error {
				_, err := b.DumpMapByName("missing")
				return err
			},
			want: `map "missing" not found`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("map operation error=%v, want error containing %q", err, tt.want)
			}
		})
	}
}

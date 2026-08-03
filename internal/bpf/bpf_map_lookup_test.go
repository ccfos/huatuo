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

package bpf

import (
	"errors"
	"testing"
)

func TestDefaultBPFMapOperationsRejectUnknownMap(t *testing.T) {
	b := &defaultBPF{}

	tests := []struct {
		name     string
		run      func(*testing.T) error
		expected string
	}{
		{
			name: "event pipe",
			run: func(t *testing.T) error {
				_, err := b.EventPipe(t.Context(), 42, 4096)
				return err
			},
			expected: "bpf: map not found: id 42",
		},
		{
			name: "event pipe by name",
			run: func(t *testing.T) error {
				_, err := b.EventPipeByName(t.Context(), "missing", 4096)
				return err
			},
			expected: `bpf: map not found: name "missing"`,
		},
		{
			name: "attach and event pipe",
			run: func(t *testing.T) error {
				_, err := b.AttachAndEventPipe(t.Context(), "missing", 4096)
				return err
			},
			expected: `bpf: map not found: name "missing"`,
		},
		{
			name: "read map",
			run: func(*testing.T) error {
				_, err := b.ReadMap(42, nil)
				return err
			},
			expected: "bpf: map not found: id 42",
		},
		{
			name: "write map items",
			run: func(*testing.T) error {
				return b.WriteMapItems(42, nil)
			},
			expected: "bpf: map not found: id 42",
		},
		{
			name: "delete map items",
			run: func(*testing.T) error {
				return b.DeleteMapItems(42, nil)
			},
			expected: "bpf: map not found: id 42",
		},
		{
			name: "dump map",
			run: func(*testing.T) error {
				_, err := b.DumpMap(42)
				return err
			},
			expected: "bpf: map not found: id 42",
		},
		{
			name: "dump map by name",
			run: func(*testing.T) error {
				_, err := b.DumpMapByName("missing")
				return err
			},
			expected: `bpf: map not found: name "missing"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run(t)
			if !errors.Is(err, ErrMapNotFound) {
				t.Fatalf("%s error = %v, want an error matching ErrMapNotFound", tt.name, err)
			}
			if err.Error() != tt.expected {
				t.Errorf("%s error = %q, want %q", tt.name, err, tt.expected)
			}
		})
	}
}

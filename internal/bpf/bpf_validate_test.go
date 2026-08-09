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

func TestValidateName(t *testing.T) {
	tests := []struct {
		name string
		input string
		wantErr bool
	}{
		{name: "plain object name", input: "iotracing.o"},
		{name: "relative output path", input: "./_output/bpf/iotracing.o"},
		{name: "empty", wantErr: true},
		{name: "parent directory", input: "..", wantErr: true},
		{name: "parent traversal", input: "../outside.o", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateName(tt.input)
			if tt.wantErr {
				if !errors.Is(err, errInvalidName) {
					t.Fatalf("validateName(%q) error = %v, want %v", tt.input, err, errInvalidName)
				}
				return
			}
			if err != nil {
				t.Fatalf("validateName(%q) error = %v", tt.input, err)
			}
		})
	}
}

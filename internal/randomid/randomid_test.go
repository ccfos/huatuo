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
	"strings"
	"testing"
)

func TestNewIfEmpty(t *testing.T) {
	tests := []struct {
		name string
		id   string
	}{
		{name: "existing id", id: "task-123"},
		{name: "whitespace preserved", id: " "},
		{name: "empty id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := NewIfEmpty(tt.id)
			if tt.id != "" {
				if id != tt.id {
					t.Fatalf("NewIfEmpty(%q) = %q, want unchanged ID", tt.id, id)
				}

				return
			}

			if len(id) != length {
				t.Fatalf("generated ID length = %d, want %d", len(id), length)
			}
			for _, char := range id {
				if !strings.ContainsRune(charset, char) {
					t.Fatalf("generated ID contains invalid character %q", char)
				}
			}
		})
	}
}

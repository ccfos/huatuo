// Copyright 2025 The HuaTuo Authors
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
package bytesutil

import (
	"bytes"
	"testing"
)

func TestToString(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		expected string
	}{
		{
			name:     "empty slice",
			input:    []byte{},
			expected: "",
		},
		{
			name:     "no null terminator",
			input:    []byte("hello"),
			expected: "hello",
		},
		{
			name:     "null terminator at end",
			input:    []byte("hello\x00"),
			expected: "hello",
		},
		{
			name:     "null terminator in middle",
			input:    []byte("hel\x00lo"),
			expected: "hel",
		},
		{
			name:     "null terminator at beginning",
			input:    []byte("\x00hello"),
			expected: "",
		},
		{
			name:     "multiple null terminators",
			input:    []byte("hel\x00lo\x00world"),
			expected: "hel",
		},
		{
			name:     "single non-null byte",
			input:    []byte{'a'},
			expected: "a",
		},
		{
			name:     "single null byte",
			input:    []byte{0},
			expected: "",
		},
		{
			name:     "all null bytes",
			input:    []byte{0, 0, 0},
			expected: "",
		},
		{
			name:     "unicode characters with null",
			input:    []byte("héllo\x00world"),
			expected: "héllo",
		},
		{
			name:     "null in multibyte rune",
			input:    []byte{0xC3, 0xA9, 0x00}, // "é" followed by null
			expected: "é",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ToString(tt.input)
			if result != tt.expected {
				t.Errorf("ToString(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func FuzzToString(f *testing.F) {
	// Seed the fuzzer with some initial inputs
	f.Add([]byte{})
	f.Add([]byte("hello"))
	f.Add([]byte("hello\x00"))
	f.Add([]byte("hel\x00lo"))
	f.Add([]byte("\x00hello"))
	f.Add([]byte("hel\x00lo\x00world"))
	f.Add([]byte{'a'})
	f.Add([]byte{0})
	f.Add([]byte{0, 0, 0})
	f.Add([]byte("héllo\x00world"))
	f.Add([]byte{0xC3, 0xA9, 0x00})

	f.Fuzz(func(t *testing.T, b []byte) {
		// Compute expected result using bytes.IndexByte
		idx := bytes.IndexByte(b, 0)
		expected := ""
		if idx == -1 {
			expected = string(b)
		} else {
			expected = string(b[:idx])
		}

		// Get actual result
		actual := ToString(b)

		// Assert equality
		if actual != expected {
			t.Errorf("ToString(%q) = %q, want %q", b, actual, expected)
		}
	})
}

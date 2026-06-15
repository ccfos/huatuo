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

package kernaddr

import "testing"

func TestFormat(t *testing.T) {
	tests := []struct {
		name string
		addr uint64
		want string
	}{
		{name: "zero", addr: 0, want: ""},
		{name: "small address", addr: 0x1234, want: "0x0000000000001234"},
		{name: "high bit address", addr: 0xffff888012345678, want: "0xffff888012345678"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Format(tt.addr); got != tt.want {
				t.Fatalf("Format(%#x) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want uint64
		ok   bool
	}{
		{name: "empty", raw: "", ok: false},
		{name: "zero", raw: "0x0000000000000000", ok: false},
		{name: "with prefix", raw: "0xffff888012345678", want: 0xffff888012345678, ok: true},
		{name: "without prefix", raw: "ffff888012345678", want: 0xffff888012345678, ok: true},
		{name: "invalid", raw: "not-hex", ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Parse(tt.raw)
			if ok != tt.ok {
				t.Fatalf("Parse(%q) ok = %v, want %v", tt.raw, ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("Parse(%q) = %#x, want %#x", tt.raw, got, tt.want)
			}
		})
	}
}

func TestFormatParseRoundTrip(t *testing.T) {
	addr := uint64(0xffff888012345678)

	got, ok := Parse(Format(addr))
	if !ok {
		t.Fatalf("Parse(Format(%#x)) ok = false, want true", addr)
	}
	if got != addr {
		t.Fatalf("Parse(Format(%#x)) = %#x, want %#x", addr, got, addr)
	}
}

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

package bytesutil

import (
	"bytes"
	"testing"
)

func TestToBytesUsesLittleEndianLayout(t *testing.T) {
	tests := []struct {
		name string
		value any
		want  []byte
	}{
		{
			name:  "unsigned integer",
			value: uint32(0x01020304),
			want:  []byte{0x04, 0x03, 0x02, 0x01},
		},
		{
			name: "fixed size struct",
			value: struct {
				ID    uint16
				Flags uint16
			}{ID: 0x1122, Flags: 0x3344},
			want: []byte{0x22, 0x11, 0x44, 0x33},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToBytes(tt.value); !bytes.Equal(got, tt.want) {
				t.Fatalf("ToBytes(%v) = %v, want %v", tt.value, got, tt.want)
			}
		})
	}
}

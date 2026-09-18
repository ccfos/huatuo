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

package events

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"strings"
	"testing"
)

func TestBytesFieldBounds(t *testing.T) {
	buf := []byte{0, 1, 2, 3, 4}
	tests := []struct {
		name      string
		rawOffset uint32
		base      uint32
		length    uint32
		want      []byte
	}{
		{name: "before base", rawOffset: 9, base: 10, length: 2},
		{name: "at end", rawOffset: 15, base: 10, length: 1},
		{name: "zero length", rawOffset: 11, base: 10},
		{name: "within bounds", rawOffset: 11, base: 10, length: 2, want: []byte{1, 2}},
		{name: "truncated at end", rawOffset: 13, base: 10, length: 10, want: []byte{3, 4}},
		{name: "huge length", rawOffset: 11, base: 10, length: 1 << 20, want: []byte{1, 2, 3, 4}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bytesField(buf, tt.rawOffset, tt.base, tt.length)
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("bytesField() = %v, want %v", got, tt.want)
			}
		})
	}
}

// acpiEvent encodes a non_standard_event trace entry the way the kernel lays it
// out, using the layout reported by
// /sys/kernel/tracing/events/ras/non_standard_event/format.
func acpiEvent(t *testing.T, fruTxtOffset, bufOffset, length uint32, msg []byte) rasEvent {
	t.Helper()

	type acpiPayload struct {
		Pad          uint64
		SecType      [16]uint8
		FRUID        [16]uint8
		FRUTxtOffset uint32
		Sev          uint8
		Pattern      [3]uint8
		Len          uint32
		BufOffset    uint32
		Msg          [DETAIL_INFO_SIZE_ACPI]byte
	}

	payload := acpiPayload{
		FRUTxtOffset: fruTxtOffset,
		BufOffset:    bufOffset,
		Len:          length,
	}
	copy(payload.Msg[:], msg)

	var encoded bytes.Buffer
	if err := binary.Write(&encoded, binary.LittleEndian, payload); err != nil {
		t.Fatal(err)
	}

	var event rasEvent
	copy(event.Info[:], encoded.Bytes())
	return event
}

func TestBuildRasAcpiTracerDataRawData(t *testing.T) {
	tests := []struct {
		name         string
		fruTxtOffset uint32
		bufOffset    uint32
		length       uint32
		msg          []byte
		wantFRU      string
		wantRaw      string
	}{
		{
			name:         "reads the buffer at buf_offset",
			fruTxtOffset: 56,
			bufOffset:    60,
			length:       3,
			msg:          []byte("fru\x00\xde\xad\xbe"),
			wantFRU:      "fru",
			wantRaw:      "de ad be",
		},
		{
			// The kernel-provided length is not trustworthy: it can run past
			// the end of the payload and must be clamped, not sliced blindly.
			name:         "length past the payload is clamped",
			fruTxtOffset: 56,
			bufOffset:    60,
			length:       1 << 20,
			msg:          []byte("fru\x00\xde\xad\xbe"),
			wantFRU:      "fru",
			wantRaw:      "de ad be" + strings.Repeat(" 00", DETAIL_INFO_SIZE_ACPI-4-3),
		},
		{
			name:         "buf_offset past the payload yields no raw data",
			fruTxtOffset: 56,
			bufOffset:    0xffff,
			length:       3,
			msg:          []byte("fru\x00"),
			wantFRU:      "fru",
		},
		{
			name:         "zero length yields no raw data",
			fruTxtOffset: 56,
			bufOffset:    60,
			length:       0,
			msg:          []byte("fru\x00"),
			wantFRU:      "fru",
		},
		{
			name:         "empty fru_text",
			fruTxtOffset: 0,
			bufOffset:    60,
			length:       3,
			msg:          []byte("fru\x00\xde\xad\xbe"),
			wantRaw:      "de ad be",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := acpiEvent(t, tt.fruTxtOffset, tt.bufOffset, tt.length, tt.msg)

			data, err := buildRasAcpiTracerData(&event)
			if err != nil {
				t.Fatalf("buildRasAcpiTracerData() error = %v", err)
			}

			var info struct {
				FruText string `json:"fru_text"`
				RawData string `json:"raw_data"`
			}
			if err := json.Unmarshal([]byte(data.Info), &info); err != nil {
				t.Fatalf("unmarshal info: %v", err)
			}
			if info.FruText != tt.wantFRU {
				t.Errorf("fru_text = %q, want %q", info.FruText, tt.wantFRU)
			}
			if info.RawData != tt.wantRaw {
				t.Errorf("raw_data = %q, want %q", info.RawData, tt.wantRaw)
			}
		})
	}
}

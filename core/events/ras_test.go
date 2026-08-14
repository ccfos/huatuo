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

func TestBuildRasAcpiTracerDataUsesBufferOffset(t *testing.T) {
	type acpiPayload struct {
		Pad          uint64
		SecType      [16]uint8
		FruID        [16]uint8
		FruTxtOffset uint32
		Sev          uint8
		Pattern      [3]uint8
		Len          uint32
		BufOffset    uint32
		Msg          [DETAIL_INFO_SIZE_ACPI]byte
	}

	payload := acpiPayload{
		FruTxtOffset: 56,
		BufOffset:    64,
		Len:          3,
	}
	copy(payload.Msg[:], "fru\x00")
	copy(payload.Msg[8:], []byte{0xde, 0xad, 0xbe})

	var encoded bytes.Buffer
	if err := binary.Write(&encoded, binary.LittleEndian, payload); err != nil {
		t.Fatal(err)
	}
	var event rasEvent
	copy(event.Info[:], encoded.Bytes())

	data, err := buildRasAcpiTracerData(&event)
	if err != nil {
		t.Fatal(err)
	}
	var info struct {
		FruText string `json:"fru_text"`
		RawData string `json:"raw_data"`
	}
	if err := json.Unmarshal([]byte(data.Info), &info); err != nil {
		t.Fatal(err)
	}
	if info.FruText != "fru" {
		t.Errorf("fru_text = %q, want %q", info.FruText, "fru")
	}
	if info.RawData != "de ad be" {
		t.Errorf("raw_data = %q, want %q", info.RawData, "de ad be")
	}
}

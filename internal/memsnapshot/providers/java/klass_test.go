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

package java

import (
	"encoding/binary"
	"os"
	"testing"
	"unsafe"
)

func TestKlassNamesDecodeModifiedUTF8(t *testing.T) {
	data := mapTestMemory(t, 1)
	address := uint64(uintptr(unsafe.Pointer(&data[0])))
	const encoded = "example/\xed\xa0\x81\xed\xb0\x80"
	const want = "example/\U00010400"
	binary.LittleEndian.PutUint32(data, 24)
	binary.LittleEndian.PutUint64(data[8:], address+32)
	binary.LittleEndian.PutUint16(data[32:], uint16(len(encoded)))
	copy(data[34:], encoded)
	metadata := &vmMeta{structs: map[string]vmStruct{
		"Klass::_layout_helper": {offset: 0},
		"Klass::_name":          {offset: 8},
		"Symbol::_length":       {offset: 0},
		"Symbol::_body[0]":      {offset: 2},
	}}
	memory := processMemory{pid: os.Getpid(), ctx: t.Context()}
	single, err := readKlass(memory, metadata, address)
	if err != nil || single.name != want {
		t.Fatalf("single Klass = %+v, %v; want %q", single, err, want)
	}
	batch := readKlassBatch(memory, metadata, []uint64{address})
	if batch[address] == nil || batch[address].name != want {
		t.Fatalf("batched Klass = %+v; want %q", batch[address], want)
	}
}

func TestDecodeModifiedUTF8(t *testing.T) {
	const supplementary = string(rune(0x1f642))
	for _, test := range []struct {
		name, input, want string
	}{
		{name: "nul", input: "\xc0\x80", want: "\x00"},
		{name: "supplementary", input: "\xed\xa0\xbd\xed\xb9\x82", want: supplementary},
		{name: "ordinary", input: "java/lang/String", want: "java/lang/String"},
		{name: "BMP", input: "example/对象", want: "example/对象"},
		{name: "unpaired surrogate", input: "\xed\xa0\xbd", want: "\ufffd"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeModifiedUTF8([]byte(test.input))
			if err != nil || got != test.want {
				t.Fatalf("decoded = %q, %v; want %q", got, err, test.want)
			}
		})
	}
	if _, err := decodeModifiedUTF8([]byte{0xe0, 0xa0}); err == nil {
		t.Fatal("truncated sequence accepted")
	}
}

func BenchmarkDecodeModifiedUTF8(b *testing.B) {
	for _, name := range []string{"java/lang/String", "example/\xed\xa0\x81\xed\xb0\x80"} {
		b.Run(name, func(b *testing.B) {
			raw := []byte(name)
			b.ReportAllocs()
			for b.Loop() {
				if _, err := decodeModifiedUTF8(raw); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

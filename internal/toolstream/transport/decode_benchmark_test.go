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

package transport

import (
	"bytes"
	"testing"

	capnp "capnproto.org/go/capnp/v3"
)

func BenchmarkFrameDecode(b *testing.B) {
	msg, seg, err := capnp.NewMessage(capnp.SingleSegment(nil))
	if err != nil {
		b.Fatal(err)
	}
	defer msg.Release()
	root, err := NewRootMessage(seg)
	if err != nil {
		b.Fatal(err)
	}
	chunk, err := root.NewChunk()
	if err != nil {
		b.Fatal(err)
	}
	if err := chunk.SetData(bytes.Repeat([]byte{'x'}, 64<<10)); err != nil {
		b.Fatal(err)
	}
	frame, err := msg.Marshal()
	if err != nil {
		b.Fatal(err)
	}
	for _, limited := range []bool{false, true} {
		name := "compatible"
		if limited {
			name = "budgeted"
		}
		b.Run(name, func(b *testing.B) {
			var budget *DecodeBudget
			if limited {
				var err error
				budget, err = NewDecodeBudget(uint64(len(frame)))
				if err != nil {
					b.Fatal(err)
				}
			}
			reader := bytes.NewReader(frame)
			decoder := newMessageDecoder(reader, uint64(len(frame)), budget)
			b.SetBytes(int64(len(frame)))
			b.ReportAllocs()
			for b.Loop() {
				reader.Reset(frame)
				message, err := decoder.Decode(b.Context())
				if err != nil {
					b.Fatal(err)
				}
				message.Release()
			}
		})
	}
}

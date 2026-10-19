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

package packet

import (
	"encoding/binary"
	"testing"
)

func benchmarkICMPv6Hdr(typ uint8, id, seq uint16, ethernet bool) Hdr {
	pkt := Hdr{EthProto: 0x86dd, RawLen: 48}
	offset := 0
	if ethernet {
		pkt.HasEthHdr = 1
		pkt.RawLen += ethernetHeaderLen
		offset = ethernetHeaderLen
		copy(pkt.Raw[:6], []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66})
		copy(pkt.Raw[6:12], []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff})
		binary.BigEndian.PutUint16(pkt.Raw[12:14], 0x86dd)
	}
	raw := pkt.Raw[offset:]
	raw[0] = 0x60
	binary.BigEndian.PutUint16(raw[4:6], 8)
	raw[6], raw[7] = 58, 64
	raw[23], raw[39] = 1, 2
	raw[40] = typ
	binary.BigEndian.PutUint16(raw[42:44], 0xabcd)
	binary.BigEndian.PutUint16(raw[44:46], id)
	binary.BigEndian.PutUint16(raw[46:48], seq)
	return pkt
}

func BenchmarkParse(b *testing.B) {
	cases := []struct {
		name   string
		packet Hdr
	}{
		{"EchoRequest", benchmarkICMPv6Hdr(128, 0x1234, 42, true)},
		{"EchoReply", benchmarkICMPv6Hdr(129, 0x1234, 42, true)},
		{"NonEcho", benchmarkICMPv6Hdr(2, 0xffff, 1500, true)},
		{"TCP", buildIPv4TCPSYNHdr()},
	}
	for i := range cases {
		tc := &cases[i]
		b.Run(tc.name, func(b *testing.B) {
			if _, err := Parse(&tc.packet); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Parse(&tc.packet); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

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
	"errors"
	"testing"
)

func TestParseVLAN(t *testing.T) {
	for _, test := range []struct {
		name      string
		tags      []uint16
		withEther bool
	}{
		{name: "802.1Q", tags: []uint16{0x8100}, withEther: true},
		{name: "QinQ", tags: []uint16{0x88a8, 0x8100}, withEther: true},
		{name: "without Ethernet", tags: []uint16{0x8100}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ip := buildIPv4TCPSYNHdr()
			pkt := Hdr{EthProto: test.tags[0], SkState: ip.SkState}
			offset := 0
			if test.withEther {
				pkt.HasEthHdr = 1
				copy(pkt.Raw[6:], []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff})
				binary.BigEndian.PutUint16(pkt.Raw[12:], test.tags[0])
				offset = ethernetHeaderLen
			}
			for index := range test.tags {
				inner := ip.EthProto
				if index+1 < len(test.tags) {
					inner = test.tags[index+1]
				}
				binary.BigEndian.PutUint16(pkt.Raw[offset:], uint16(index+1))
				binary.BigEndian.PutUint16(pkt.Raw[offset+2:], inner)
				offset += 4
			}
			copy(pkt.Raw[offset:], ip.Raw[:ip.RawLen])
			pkt.RawLen = uint8(offset) + ip.RawLen
			parsed, err := Parse(&pkt)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Label != "IPv4/TCP" || parsed.IPv4 == nil || parsed.TCP == nil ||
				parsed.IPv4.Saddr.String() != "10.0.0.1" ||
				parsed.TCP.Sport != 12345 || parsed.TCP.Dport != 80 {
				t.Fatalf("tagged frame = %+v", parsed)
			}
			if test.withEther && (parsed.Ether == nil || parsed.Ether.Saddr != "aa:bb:cc:dd:ee:ff") {
				t.Fatalf("Ethernet header lost: %+v", parsed.Ether)
			}
			if !test.withEther && parsed.Ether != nil {
				t.Fatalf("synthetic Ethernet header leaked: %+v", parsed.Ether)
			}
		})
	}
}

func TestParseTruncatedVLAN(t *testing.T) {
	pkt := Hdr{EthProto: 0x8100, RawLen: 3}
	if parsed, err := Parse(&pkt); parsed != nil || !errors.Is(err, ErrNoLayers) {
		t.Fatalf("truncated tag = %+v, %v; want no layers", parsed, err)
	}
}

func BenchmarkParseIPv4TCP(b *testing.B) {
	pkt := buildIPv4TCPSYNHdr()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Parse(&pkt); err != nil {
			b.Fatal(err)
		}
	}
}

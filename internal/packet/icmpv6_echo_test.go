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
	"encoding/json"
	"strings"
	"testing"
)

func buildICMPv6TestHdr(typ uint8, id, seq uint16, ethernet bool) Hdr {
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

func TestParseICMPv6EchoIdentifiers(t *testing.T) {
	for _, typ := range []uint8{128, 129} {
		for _, ethernet := range []bool{false, true} {
			pkt := buildICMPv6TestHdr(typ, 0x1234, 42, ethernet)
			parsed, err := Parse(&pkt)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.ICMP == nil || parsed.ICMP.ID != 0x1234 || parsed.ICMP.Seq != 42 {
				t.Fatalf("type %d ethernet=%t: ICMP = %+v, want id=4660 seq=42", typ, ethernet, parsed.ICMP)
			}
			if parsed.Label != "IPv6/ICMPv6" || parsed.ICMP.Checksum != 0xabcd {
				t.Errorf("label/checksum changed: %+v", parsed)
			}
			if !strings.Contains(parsed.String(), "id=4660 seq=42") {
				t.Errorf("text output is missing echo identity: %s", parsed)
			}
			encoded, err := json.Marshal(parsed.ICMP)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), `"id":4660`) || !strings.Contains(string(encoded), `"seq":42`) {
				t.Errorf("JSON is missing echo identity: %s", encoded)
			}
		}
	}
}

func TestParseICMPv6NonEchoAndTruncatedPacketsHaveNoEchoIdentity(t *testing.T) {
	seed := buildICMPv6TestHdr(128, 0x1234, 42, false)
	if _, err := Parse(&seed); err != nil {
		t.Fatal(err)
	}
	for _, typ := range []uint8{1, 2, 3, 4, 133} {
		pkt := buildICMPv6TestHdr(typ, 0xffff, 1500, false)
		parsed, err := Parse(&pkt)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.ICMP == nil || parsed.ICMP.ID != 0 || parsed.ICMP.Seq != 0 {
			t.Errorf("non-echo type %d leaked echo identity: %+v", typ, parsed.ICMP)
		}
	}
	truncated := buildICMPv6TestHdr(128, 0x1234, 42, false)
	truncated.RawLen = 46
	parsed, err := Parse(&truncated)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.ICMP == nil || parsed.ICMP.ID != 0 || parsed.ICMP.Seq != 0 {
		t.Errorf("truncated echo exposed incomplete identity: %+v", parsed.ICMP)
	}
}

func BenchmarkParseICMPv6Echo(b *testing.B) {
	pkt := buildICMPv6TestHdr(128, 0x1234, 42, false)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Parse(&pkt); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseIPv4TCPAfterICMPv6Echo(b *testing.B) {
	pkt := buildIPv4TCPSYNHdr()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Parse(&pkt); err != nil {
			b.Fatal(err)
		}
	}
}

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

// buildIPv4UDPHdr builds an options-free IPv4/UDP frame (IHL=5) so a leaked
// pooled decoder would be distinguishable from the frame under test.
func buildIPv4UDPHdr(ttl uint8, saddr, daddr [4]byte) Hdr {
	pkt := Hdr{EthProto: 0x0800, RawLen: 20 + 8}
	r := pkt.Raw[:]
	r[0] = 0x45 // version=4, ihl=5
	binary.BigEndian.PutUint16(r[2:], 28)
	r[8] = ttl
	r[9] = 17 // UDP
	copy(r[12:16], saddr[:])
	copy(r[16:20], daddr[:])
	binary.BigEndian.PutUint16(r[20:], 5353)
	binary.BigEndian.PutUint16(r[22:], 53)
	binary.BigEndian.PutUint16(r[24:], 8)
	return pkt
}

// buildIPv4OptionsTCPSYNHdr builds an IPv4/TCP SYN with IHL=6 whose option
// area is the RFC 791 End-of-Options terminator (00 00 00 00).
func buildIPv4OptionsTCPSYNHdr() Hdr {
	pkt := Hdr{EthProto: 0x0800, RawLen: 24 + 20}
	r := pkt.Raw[:]
	r[0] = 0x46 // version=4, ihl=6
	binary.BigEndian.PutUint16(r[2:], 44)
	r[6] = 0x40 // DF
	r[8] = 64   // TTL
	r[9] = 6    // TCP
	copy(r[12:16], []byte{10, 0, 0, 1})
	copy(r[16:20], []byte{10, 0, 0, 2})
	// r[20:24] left as End-of-Options padding (0x00).
	tcp := r[24:]
	binary.BigEndian.PutUint16(tcp[0:], 1234)
	binary.BigEndian.PutUint16(tcp[2:], 80)
	binary.BigEndian.PutUint32(tcp[4:], 1)
	tcp[12] = 0x50 // data offset 5
	tcp[13] = TCPFlagSYN
	return pkt
}

// buildEthernetIPv4OptionsUDPHdr builds an Ethernet+IPv4(IHL=6, EOL options)+UDP
// frame for the HasEthHdr==1 path.
func buildEthernetIPv4OptionsUDPHdr() Hdr {
	pkt := Hdr{HasEthHdr: 1, RawLen: ethernetHeaderLen + 24 + 8}
	r := pkt.Raw[:]
	copy(r[0:6], []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66})
	copy(r[6:12], []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff})
	binary.BigEndian.PutUint16(r[12:], 0x0800)
	ip := r[ethernetHeaderLen:]
	ip[0] = 0x46 // version=4, ihl=6
	binary.BigEndian.PutUint16(ip[2:], 32)
	ip[8] = 64
	ip[9] = 17 // UDP
	copy(ip[12:16], []byte{10, 1, 0, 1})
	copy(ip[16:20], []byte{10, 1, 0, 2})
	// ip[20:24] left as End-of-Options padding (0x00).
	udp := ip[24:]
	binary.BigEndian.PutUint16(udp[0:], 5353)
	binary.BigEndian.PutUint16(udp[2:], 53)
	binary.BigEndian.PutUint16(udp[4:], 8)
	return pkt
}

// TestParseIPv4OptionsWithEthHdr covers the HasEthHdr==1 branch, where the
// parser slices pkt.Raw directly: the option rewrite must not mutate it.
func TestParseIPv4OptionsWithEthHdr(t *testing.T) {
	pkt := buildEthernetIPv4OptionsUDPHdr()
	snapshot := pkt.Raw

	p, err := Parse(&pkt)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.IPv4 == nil {
		t.Fatalf("IPv4: want non-nil, got nil")
	}
	if p.IPv4.Version != 4 || p.IPv4.IHL != 6 || p.IPv4.TTL != 64 {
		t.Errorf("IPv4 header: want version=4 ihl=6 ttl=64, got version=%d ihl=%d ttl=%d",
			p.IPv4.Version, p.IPv4.IHL, p.IPv4.TTL)
	}
	if p.IPv4.Protocol != "UDP" {
		t.Errorf("IPv4.Protocol: want UDP, got %q", p.IPv4.Protocol)
	}
	if p.UDP == nil {
		t.Fatalf("UDP: want non-nil, got nil")
	}
	if pkt.Raw != snapshot {
		t.Errorf("Parse mutated the caller's Raw buffer")
	}
}

// TestParseIPv4Options: an IHL>5 IPv4 frame must still yield its own fixed
// header fields and its L4 layer. gopacket v1.2.0's IPv4 decoder returns on the
// first End-of-Options byte before assigning them, so the pooled decoder leaks
// the previous packet's Version/TTL/Protocol and drops TCP.
func TestParseIPv4Options(t *testing.T) {
	// Warm the pool with a frame whose fixed fields all differ from the frame
	// under test: TTL=32/UDP/192.168.0.9 rather than TTL=64/TCP/10.0.0.1.
	prev := buildIPv4UDPHdr(32, [4]byte{192, 0, 0, 9}, [4]byte{192, 0, 0, 10})
	if _, err := Parse(&prev); err != nil {
		t.Fatalf("warm-up Parse: %v", err)
	}

	pkt := buildIPv4OptionsTCPSYNHdr()
	p, err := Parse(&pkt)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if p.IPv4 == nil {
		t.Fatalf("IPv4: want non-nil, got nil")
	}
	if p.IPv4.Version != 4 {
		t.Errorf("IPv4.Version: want 4, got %d", p.IPv4.Version)
	}
	if p.IPv4.IHL != 6 {
		t.Errorf("IPv4.IHL: want 6, got %d", p.IPv4.IHL)
	}
	if p.IPv4.TTL != 64 {
		t.Errorf("IPv4.TTL: want 64, got %d", p.IPv4.TTL)
	}
	if p.IPv4.Protocol != "TCP" {
		t.Errorf("IPv4.Protocol: want TCP, got %q", p.IPv4.Protocol)
	}
	if got := p.IPv4.Saddr.String(); got != "10.0.0.1" {
		t.Errorf("IPv4.Saddr: want 10.0.0.1, got %s", got)
	}
	if got := p.IPv4.Daddr.String(); got != "10.0.0.2" {
		t.Errorf("IPv4.Daddr: want 10.0.0.2, got %s", got)
	}
	if p.TCP == nil {
		t.Fatalf("TCP: want non-nil, got nil")
	}
	if p.TCP.Sport != 1234 || p.TCP.Dport != 80 {
		t.Errorf("TCP ports: want 1234>80, got %d>%d", p.TCP.Sport, p.TCP.Dport)
	}
	if got := p.Label; got != "IPv4/TCP" {
		t.Errorf("Label: want IPv4/TCP, got %s", got)
	}
}

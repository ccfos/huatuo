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

func buildIPv4TCPSYNHdr() Hdr {
	pkt := Hdr{
		EthProto: 0x0800,
		RawLen:   40, // 20 IPv4 + 20 TCP
		SkState:  1,  // ESTABLISHED
	}
	// IPv4 header at Raw[0..19]
	pkt.Raw[0] = 0x45                               // version=4, ihl=5
	pkt.Raw[1] = 0x10                               // TOS
	binary.BigEndian.PutUint16(pkt.Raw[2:], 40)     // total length
	binary.BigEndian.PutUint16(pkt.Raw[4:], 0x1234) // ID
	pkt.Raw[6] = 0x40                               // flags=DF, fragoffset=0
	pkt.Raw[8] = 64                                 // TTL
	pkt.Raw[9] = 6                                  // protocol=TCP
	pkt.Raw[12] = 10                                // src 10.0.0.1
	pkt.Raw[15] = 1
	pkt.Raw[16] = 10 // dst 10.0.0.2
	pkt.Raw[19] = 2
	// TCP header at Raw[20..39]
	binary.BigEndian.PutUint16(pkt.Raw[20:], 12345)  // sport
	binary.BigEndian.PutUint16(pkt.Raw[22:], 80)     // dport
	binary.BigEndian.PutUint32(pkt.Raw[24:], 1)      // seq
	pkt.Raw[32] = 0x50                               // data offset=5 (20 bytes)
	pkt.Raw[33] = 0x02                               // SYN
	binary.BigEndian.PutUint16(pkt.Raw[34:], 0x2000) // window

	return pkt
}

func TestParseIPv4TCP(t *testing.T) {
	pkt := buildIPv4TCPSYNHdr()
	p, err := Parse(&pkt)

	if err != nil {
		t.Fatalf("Parse err: %v", err)
	}
	if p == nil {
		t.Fatalf("Packet: want non-nil, got nil")
	}
	if p.IPv4 == nil {
		t.Fatalf("IPv4: want non-nil, got nil")
	}
	if p.IPv6 != nil {
		t.Errorf("IPv6: want nil, got %+v", p.IPv6)
	}
	if got := p.IPv4.Src.String(); got != "10.0.0.1" {
		t.Errorf("IPv4.Src: want 10.0.0.1, got %s", got)
	}
	if got := p.IPv4.Dst.String(); got != "10.0.0.2" {
		t.Errorf("IPv4.Dst: want 10.0.0.2, got %s", got)
	}
	if p.IPv4.Version != 4 {
		t.Errorf("IPv4.Version: want 4, got %d", p.IPv4.Version)
	}
	if p.IPv4.IHL != 5 {
		t.Errorf("IPv4.IHL: want 5, got %d", p.IPv4.IHL)
	}
	if p.IPv4.TOS != 0x10 {
		t.Errorf("IPv4.TOS: want 0x10, got 0x%x", p.IPv4.TOS)
	}
	if p.IPv4.Length != 40 {
		t.Errorf("IPv4.Length: want 40, got %d", p.IPv4.Length)
	}
	if p.IPv4.ID != 0x1234 {
		t.Errorf("IPv4.ID: want 0x1234, got 0x%x", p.IPv4.ID)
	}
	if p.IPv4.Flags != "DF" {
		t.Errorf("IPv4.Flags: want DF, got %s", p.IPv4.Flags)
	}
	if p.IPv4.TTL != 64 {
		t.Errorf("IPv4.TTL: want 64, got %d", p.IPv4.TTL)
	}
	if p.IPv4.Protocol != "TCP" {
		t.Errorf("IPv4.Protocol: want TCP, got %s", p.IPv4.Protocol)
	}
	if p.TCP == nil {
		t.Fatalf("TCP: want non-nil, got nil")
	}
	if p.TCP.SrcPort != 12345 {
		t.Errorf("TCP.SrcPort: want 12345, got %d", p.TCP.SrcPort)
	}
	if p.TCP.DstPort != 80 {
		t.Errorf("TCP.DstPort: want 80, got %d", p.TCP.DstPort)
	}
	if p.TCP.DataOffset != 5 {
		t.Errorf("TCP.DataOffset: want 5, got %d", p.TCP.DataOffset)
	}
	if p.TCP.Flags != "SYN" {
		t.Errorf("TCP.Flags: want SYN, got %s", p.TCP.Flags)
	}
	if p.TCP.Window != 0x2000 {
		t.Errorf("TCP.Window: want 0x2000, got 0x%x", p.TCP.Window)
	}
	if p.TCP.SkState != "ESTABLISHED" {
		t.Errorf("TCP.SkState: want ESTABLISHED, got %s", p.TCP.SkState)
	}
	if got := p.Label; got != "IPv4/TCP" {
		t.Errorf("Label: want IPv4/TCP, got %s", got)
	}
}

func TestParseIPv4UDP(t *testing.T) {
	pkt := Hdr{EthProto: 0x0800, RawLen: 28}       // 20 IPv4 + 8 UDP
	pkt.Raw[0] = 0x45                              // version=4, ihl=5
	pkt.Raw[9] = 17                                // protocol=UDP
	pkt.Raw[12], pkt.Raw[15] = 10, 1               // src 10.0.0.1
	pkt.Raw[16], pkt.Raw[19] = 10, 2               // dst 10.0.0.2
	binary.BigEndian.PutUint16(pkt.Raw[20:], 5353) // sport
	binary.BigEndian.PutUint16(pkt.Raw[22:], 53)   // dport
	binary.BigEndian.PutUint16(pkt.Raw[24:], 8)    // length

	p, err := Parse(&pkt)
	if err != nil {
		t.Fatalf("Parse err: %v", err)
	}
	if p.IPv4 == nil || p.UDP == nil {
		t.Fatalf("want IPv4+UDP, got %+v", p)
	}
	if p.UDP.SrcPort != 5353 || p.UDP.DstPort != 53 {
		t.Errorf("ports: want 5353>53, got %d>%d", p.UDP.SrcPort, p.UDP.DstPort)
	}
	if got := p.Label; got != "IPv4/UDP" {
		t.Errorf("Label: want IPv4/UDP, got %s", got)
	}
}

func TestParseARP(t *testing.T) {
	pkt := Hdr{EthProto: 0x0806, RawLen: 28}
	// ARP header: htype=1, ptype=0x0800, hlen=6, plen=4, op=1
	binary.BigEndian.PutUint16(pkt.Raw[0:], 1)
	binary.BigEndian.PutUint16(pkt.Raw[2:], 0x0800)
	pkt.Raw[4] = 6
	pkt.Raw[5] = 4
	binary.BigEndian.PutUint16(pkt.Raw[6:], 1) // request
	// sender MAC at Raw[8..13]
	copy(pkt.Raw[8:], []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff})
	// sender IP at Raw[14..17]
	copy(pkt.Raw[14:], []byte{10, 0, 0, 1})
	// target MAC at Raw[18..23]
	copy(pkt.Raw[18:], []byte{0, 0, 0, 0, 0, 0})
	// target IP at Raw[24..27]
	copy(pkt.Raw[24:], []byte{10, 0, 0, 2})

	p, err := Parse(&pkt)
	if err != nil {
		t.Fatalf("Parse err: %v", err)
	}
	if p.ARP == nil {
		t.Fatalf("ARP: want non-nil, got nil")
	}
	if p.ARP.Operation != "request" {
		t.Errorf("Operation: want request, got %s", p.ARP.Operation)
	}
	if p.ARP.Protocol != "IPv4" {
		t.Errorf("Protocol: want IPv4, got %s", p.ARP.Protocol)
	}
	if p.ARP.HwAddressSize != 6 {
		t.Errorf("HwAddressSize: want 6, got %d", p.ARP.HwAddressSize)
	}
	if p.ARP.ProtAddressSize != 4 {
		t.Errorf("ProtAddressSize: want 4, got %d", p.ARP.ProtAddressSize)
	}
	if got := p.ARP.SenderIP.String(); got != "10.0.0.1" {
		t.Errorf("SenderIP: want 10.0.0.1, got %s", got)
	}
	if got := p.ARP.TargetIP.String(); got != "10.0.0.2" {
		t.Errorf("TargetIP: want 10.0.0.2, got %s", got)
	}
	if got := p.Label; got != "ARP" {
		t.Errorf("Label: want ARP, got %s", got)
	}
}

// TestParseWithEthHdr exercises the HasEthHdr=1 path: the parser
// reads MACs and EtherType from the raw frame instead of synthesizing them.
func TestParseWithEthHdr(t *testing.T) {
	pkt := Hdr{HasEthHdr: 1, RawLen: ethernetHeaderLen + 28} // eth + IPv4 + UDP
	// Ethernet header: dst, src, ethertype
	copy(pkt.Raw[0:], []byte{0x11, 0x22, 0x33, 0x44, 0x55, 0x66})
	copy(pkt.Raw[6:], []byte{0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff})
	binary.BigEndian.PutUint16(pkt.Raw[12:], 0x0800)
	// Minimal IPv4 header at Raw[14..33]
	pkt.Raw[14] = 0x45               // version=4, ihl=5
	pkt.Raw[23] = 17                 // protocol=UDP
	pkt.Raw[26], pkt.Raw[29] = 10, 1 // src 10.0.0.1
	pkt.Raw[30], pkt.Raw[33] = 10, 2 // dst 10.0.0.2
	// UDP header at Raw[34..41]
	binary.BigEndian.PutUint16(pkt.Raw[34:], 5353)
	binary.BigEndian.PutUint16(pkt.Raw[36:], 53)
	binary.BigEndian.PutUint16(pkt.Raw[38:], 8)

	p, err := Parse(&pkt)
	if err != nil {
		t.Fatalf("Parse err: %v", err)
	}
	if p.Ether == nil {
		t.Fatalf("Ether: want non-nil, got nil")
	}
	if p.Ether.Src != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("Ether.Src: want aa:bb:cc:dd:ee:ff, got %s", p.Ether.Src)
	}
	if p.Ether.Dst != "11:22:33:44:55:66" {
		t.Errorf("Ether.Dst: want 11:22:33:44:55:66, got %s", p.Ether.Dst)
	}
	if p.Ether.Type != "IPv4" {
		t.Errorf("Ether.Type: want IPv4, got %s", p.Ether.Type)
	}
}

func TestParseUnknown(t *testing.T) {
	pkt := Hdr{EthProto: 0x9999}
	p, err := Parse(&pkt)
	if p != nil {
		t.Errorf("Packet: want nil, got %v", p)
	}
	if !errors.Is(err, ErrNoLayers) {
		t.Errorf("err: want ErrNoLayers, got %v", err)
	}
}

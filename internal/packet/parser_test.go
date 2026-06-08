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

func buildIPv4TCPSYNPacketHdr() PacketHdr {
	pkt := PacketHdr{
		EthProto: 0x0800,
		RawLen:   40, // 20 IPv4 + 20 TCP
		SkState:  1,  // ESTABLISHED
	}
	// IPv4 header at Raw[0..19]
	pkt.Raw[0] = 0x45 // version=4, ihl=5
	pkt.Raw[9] = 6    // protocol=TCP
	pkt.Raw[12] = 10  // src 10.0.0.1
	pkt.Raw[15] = 1
	pkt.Raw[16] = 10 // dst 10.0.0.2
	pkt.Raw[19] = 2
	// TCP header at Raw[20..39]
	binary.BigEndian.PutUint16(pkt.Raw[20:], 12345) // sport
	binary.BigEndian.PutUint16(pkt.Raw[22:], 80)    // dport
	binary.BigEndian.PutUint32(pkt.Raw[24:], 1)     // seq
	pkt.Raw[32] = 0x50                              // data offset=5 (20 bytes)
	pkt.Raw[33] = 0x02                              // SYN
	return pkt
}

func TestParsePacketHdrIPv4TCP(t *testing.T) {
	pkt := buildIPv4TCPSYNPacketHdr()
	pktType, info := ParsePacketHdr(&pkt)

	if pktType != PacketTypeIPv4TCP {
		t.Fatalf("pktType: want %d, got %d", PacketTypeIPv4TCP, pktType)
	}
	tcp, ok := info.(*TCPInfo)
	if !ok {
		t.Fatalf("info type: want *TCPInfo, got %T", info)
	}
	if tcp.Saddr != "10.0.0.1" {
		t.Errorf("saddr: want 10.0.0.1, got %s", tcp.Saddr)
	}
	if tcp.Daddr != "10.0.0.2" {
		t.Errorf("daddr: want 10.0.0.2, got %s", tcp.Daddr)
	}
	if tcp.Sport != 12345 {
		t.Errorf("sport: want 12345, got %d", tcp.Sport)
	}
	if tcp.Dport != 80 {
		t.Errorf("dport: want 80, got %d", tcp.Dport)
	}
	if tcp.Flags != "SYN" {
		t.Errorf("flags: want SYN, got %s", tcp.Flags)
	}
	if tcp.SkState != "ESTABLISHED" {
		t.Errorf("sk_state: want ESTABLISHED, got %s", tcp.SkState)
	}
}

func TestParsePacketHdrUnknown(t *testing.T) {
	pkt := PacketHdr{EthProto: 0x9999}
	pktType, info := ParsePacketHdr(&pkt)
	if pktType != PacketTypeUnknown {
		t.Errorf("want PacketTypeUnknown, got %d", pktType)
	}
	if info != nil {
		t.Errorf("want nil info, got %v", info)
	}
}

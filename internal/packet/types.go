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
	"encoding/json"
	"fmt"
)

// PacketType is the L3+L4 protocol combination parsed from a BPF packet header.
type PacketType uint8

const (
	PacketTypeUnknown    PacketType = 0
	PacketTypeIPv4TCP    PacketType = 1
	PacketTypeIPv4UDP    PacketType = 2
	PacketTypeIPv4ICMP   PacketType = 3
	PacketTypeIPv6TCP    PacketType = 4
	PacketTypeIPv6UDP    PacketType = 5
	PacketTypeIPv6ICMPv6 PacketType = 6
	PacketTypeARP        PacketType = 7
)

var packetTypeNames = [...]string{
	PacketTypeUnknown:    "unknown",
	PacketTypeIPv4TCP:    "IPv4/TCP",
	PacketTypeIPv4UDP:    "IPv4/UDP",
	PacketTypeIPv4ICMP:   "IPv4/ICMP",
	PacketTypeIPv6TCP:    "IPv6/TCP",
	PacketTypeIPv6UDP:    "IPv6/UDP",
	PacketTypeIPv6ICMPv6: "IPv6/ICMPv6",
	PacketTypeARP:        "ARP",
}

func (p PacketType) String() string {
	if int(p) < len(packetTypeNames) {
		return packetTypeNames[p]
	}
	return fmt.Sprintf("unknown(%d)", p)
}

func (p PacketType) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.String())
}

func (p *PacketType) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}

	for i, name := range packetTypeNames {
		if name == s {
			*p = PacketType(i)
			return nil
		}
	}

	*p = PacketTypeUnknown
	return nil
}

// RawCapacity is the size of the raw packet buffer, matching PKT_RAW_LEN in bpf/dropwatch.c.
const RawCapacity = 120

// PacketHdr mirrors struct packet_hdr in bpf/dropwatch.c.
type PacketHdr struct {
	EthProto  uint16
	RawLen    uint8
	HasEthHdr uint8 // 1: Raw starts with Ethernet header; 0: starts at L3
	SkState   uint8
	Raw       [RawCapacity]byte
}

// PacketInfo holds protocol-specific fields parsed from PacketHdr.
type PacketInfo any

type TCPInfo struct {
	SrcMAC  string `json:"smac,omitempty"`
	DstMAC  string `json:"dmac,omitempty"`
	Saddr   string `json:"saddr"`
	Daddr   string `json:"daddr"`
	Sport   uint16 `json:"sport"`
	Dport   uint16 `json:"dport"`
	SkState string `json:"tcp_state"`
	Seq     uint32 `json:"seq"`
	AckSeq  uint32 `json:"ack"`
	Window  uint16 `json:"window"`
	Flags   string `json:"flags"`
}

type UDPInfo struct {
	SrcMAC   string `json:"smac,omitempty"`
	DstMAC   string `json:"dmac,omitempty"`
	Saddr    string `json:"saddr"`
	Daddr    string `json:"daddr"`
	Sport    uint16 `json:"sport"`
	Dport    uint16 `json:"dport"`
	Len      uint16 `json:"len"`
	Checksum uint16 `json:"chksum"`
}

type ICMPInfo struct {
	SrcMAC   string `json:"smac,omitempty"`
	DstMAC   string `json:"dmac,omitempty"`
	Saddr    string `json:"saddr"`
	Daddr    string `json:"daddr"`
	ICMPType string `json:"icmp_type"`
	ID       uint16 `json:"id,omitempty"`
	Seq      uint16 `json:"seq,omitempty"`
	Checksum uint16 `json:"checksum,omitempty"`
}

type ARPInfo struct {
	SrcMAC    string `json:"smac,omitempty"`
	DstMAC    string `json:"dmac,omitempty"`
	Operation string `json:"operation"`
	SenderMAC string `json:"sha"`
	SenderIP  string `json:"spa"`
	TargetMAC string `json:"tha"`
	TargetIP  string `json:"tpa"`
}

func (t *TCPInfo) Detail() string {
	return fmt.Sprintf("[%s] %s:%d > %s:%d sk=%s seq=%d ack=%d win=%d smac=%s dmac=%s",
		t.Flags, t.Saddr, t.Sport, t.Daddr, t.Dport, t.SkState, t.Seq, t.AckSeq, t.Window, t.SrcMAC, t.DstMAC)
}

func (t *UDPInfo) Detail() string {
	return fmt.Sprintf("[UDP] %s:%d > %s:%d len=%d chk=0x%04x smac=%s dmac=%s",
		t.Saddr, t.Sport, t.Daddr, t.Dport, t.Len, t.Checksum, t.SrcMAC, t.DstMAC)
}

func (t *ICMPInfo) Detail() string {
	return fmt.Sprintf("[ICMP %s] %s > %s id=%d seq=%d chk=0x%04x smac=%s dmac=%s",
		t.ICMPType, t.Saddr, t.Daddr, t.ID, t.Seq, t.Checksum, t.SrcMAC, t.DstMAC)
}

func (t *ARPInfo) Detail() string {
	return fmt.Sprintf("[ARP %s] %s > %s sender=%s target=%s smac=%s dmac=%s",
		t.Operation, t.SenderIP, t.TargetIP, t.SenderMAC, t.TargetMAC, t.SrcMAC, t.DstMAC)
}

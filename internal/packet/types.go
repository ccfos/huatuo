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
	"fmt"
	"net"
	"strconv"
	"strings"
)

// RawCapacity is the size of the raw packet buffer, matching PKT_RAW_LEN in bpf/dropwatch.c.
const RawCapacity = 120

// Hdr mirrors struct packet_hdr in bpf/dropwatch.c.
type Hdr struct {
	EthProto  uint16
	RawLen    uint8
	HasEthHdr uint8 // 1: Raw starts with Ethernet header; 0: starts at L3
	SkState   uint8
	Raw       [RawCapacity]byte
}

// Packet is a layered representation of a parsed network packet. Each layer
// pointer is non-nil iff that layer was present in the parsed frame, so any
// protocol combination (ether+ipv4+tcp, ether+ipv6+udp, arp, …) is expressed
// by the set of non-nil fields rather than by a separate type per combination.
//
// JSON output mirrors the struct layout: `{"ether":{...},"ipv4":{...},...}`.
// Missing layers are omitted via `omitempty`.
type Packet struct {
	Ether *Ether `json:"ether,omitempty"`
	IPv4  *IPv4  `json:"ipv4,omitempty"`
	IPv6  *IPv6  `json:"ipv6,omitempty"`
	TCP   *TCP   `json:"tcp,omitempty"`
	UDP   *UDP   `json:"udp,omitempty"`
	ICMP  *ICMP  `json:"icmp,omitempty"`
	ARP   *ARP   `json:"arp,omitempty"`
}

// Ether holds Ethernet-layer fields. Type is the parsed EtherType name
// (e.g. "IPv4", "IPv6", "ARP"); the raw hex value lives at the top level as
// packet_eth_proto so the two are complementary. Length is non-zero only for
// 802.3 framing — Ethernet II frames carry an EtherType in that slot instead.
type Ether struct {
	Src    string `json:"src"` // "aa:bb:cc:dd:ee:ff"
	Dst    string `json:"dst"` // "aa:bb:cc:dd:ee:ff"
	Type   string `json:"type"`
	Length uint16 `json:"len,omitempty"`
}

// IPv4 holds IPv4-layer fields. Flags renders the bitfield as
// "DF|MF"-style text; Protocol carries gopacket's name for the L4 protocol
// (e.g. "TCP", "UDP", "ICMPv4").
type IPv4 struct {
	Version    uint8  `json:"version"`
	IHL        uint8  `json:"ihl"`
	TOS        uint8  `json:"tos"`
	Length     uint16 `json:"len"`
	ID         uint16 `json:"id"`
	Flags      string `json:"flags,omitempty"`
	FragOffset uint16 `json:"frag_offset,omitempty"`
	TTL        uint8  `json:"ttl"`
	Protocol   string `json:"protocol"`
	Checksum   uint16 `json:"checksum"`
	Src        net.IP `json:"src"`
	Dst        net.IP `json:"dst"`
}

// IPv6 holds IPv6-layer fields. NextHeader is gopacket's name for the
// next header (e.g. "TCP", "ICMPv6", "IPv6-HopByHop"); Length is the payload
// length excluding the 40-byte fixed header.
type IPv6 struct {
	Version      uint8  `json:"version"`
	TrafficClass uint8  `json:"traffic_class"`
	FlowLabel    uint32 `json:"flow_label"`
	Length       uint16 `json:"len"`
	NextHeader   string `json:"next_header"`
	HopLimit     uint8  `json:"hop_limit"`
	Src          net.IP `json:"src"`
	Dst          net.IP `json:"dst"`
}

// TCP holds L4 TCP fields. SkState originates from the BPF probe
// (kernel sk_state) and is not part of the wire packet.
type TCP struct {
	SrcPort    uint16 `json:"sport"`
	DstPort    uint16 `json:"dport"`
	Seq        uint32 `json:"seq"`
	Ack        uint32 `json:"ack"`
	DataOffset uint8  `json:"data_offset"`
	Flags      string `json:"flags"`
	Window     uint16 `json:"window"`
	Checksum   uint16 `json:"checksum"`
	Urgent     uint16 `json:"urgent,omitempty"`
	SkState    string `json:"sk_state,omitempty"`
}

// UDP holds L4 UDP fields.
type UDP struct {
	SrcPort  uint16 `json:"sport"`
	DstPort  uint16 `json:"dport"`
	Length   uint16 `json:"len"`
	Checksum uint16 `json:"checksum"`
}

// ICMP is shared between ICMPv4 and ICMPv6: the L3 layer (IPv4 vs IPv6)
// is the discriminator. Type is gopacket's pre-rendered TypeCode string
// (e.g. "EchoRequest"); Code is the raw code byte. ID/Seq carry the ICMPv4
// echo fields when present; ICMPv6 packets populate only Type/Code/Checksum.
type ICMP struct {
	Type     string `json:"type"`
	Code     uint8  `json:"code"`
	Checksum uint16 `json:"checksum"`
	ID       uint16 `json:"id,omitempty"`
	Seq      uint16 `json:"seq,omitempty"`
}

// ARP holds ARP request/reply fields. AddrType and Protocol are the
// parsed link/network layer names (e.g. "Ethernet", "IPv4").
type ARP struct {
	AddrType        string `json:"addr_type"`
	Protocol        string `json:"protocol"`
	HwAddressSize   uint8  `json:"hw_address_size"`
	ProtAddressSize uint8  `json:"prot_address_size"`
	Operation       string `json:"operation"`
	SenderMAC       string `json:"sender_mac"` // "aa:bb:cc:dd:ee:ff"
	SenderIP        net.IP `json:"sender_ip"`
	TargetMAC       string `json:"target_mac"` // "aa:bb:cc:dd:ee:ff"
	TargetIP        net.IP `json:"target_ip"`
}

// Label returns a short protocol-combination tag like "IPv4/TCP", "IPv6/UDP",
// "ARP", or "unknown". Used as the leading token of String().
func (p *Packet) Label() string {
	if p == nil {
		return "unknown"
	}

	if p.ARP != nil {
		return "ARP"
	}

	var l3 string
	switch {
	case p.IPv4 != nil:
		l3 = "IPv4"
	case p.IPv6 != nil:
		l3 = "IPv6"
	}

	var l4 string
	switch {
	case p.TCP != nil:
		l4 = "TCP"
	case p.UDP != nil:
		l4 = "UDP"
	case p.ICMP != nil:
		l4 = "ICMP"
		if p.IPv6 != nil {
			l4 = "ICMPv6"
		}
	}

	switch {
	case l3 != "" && l4 != "":
		return l3 + "/" + l4
	case l3 != "":
		return l3
	case l4 != "":
		return l4
	}

	return "unknown"
}

// String renders the packet in protocol-stack order — ether → L3 (ARP / IPv4 /
// IPv6) → L4 (TCP / UDP / ICMP) — matching how the bytes appear on the wire:
//
//	IPv4/TCP smac=aa:.. dmac=11:.. 10.0.0.1:1234 > 10.0.0.2:80 [SYN] seq=1 ack=0 win=0 sk=ESTABLISHED
//	IPv6/UDP [::1]:53 > [::2]:1234 len=64 chk=0xabcd
//	ARP smac=.. dmac=.. request sender=10.0.0.1/aa:.. target=10.0.0.2/00:00:..
//	IPv4 smac=.. dmac=.. 10.0.0.1 > 10.0.0.2
//
// The leading Label() token doubles as dropwatch's protocol-type column.
// Missing layers are omitted gracefully — frames with only L3 print just src > dst.
func (p *Packet) String() string {
	if p == nil {
		return "unknown"
	}

	var b strings.Builder

	b.WriteString(p.Label())

	if p.Ether != nil {
		fmt.Fprintf(&b, " smac=%s dmac=%s", p.Ether.Src, p.Ether.Dst)
	}

	if p.ARP != nil {
		fmt.Fprintf(&b, " %s sender=%s/%s target=%s/%s",
			p.ARP.Operation,
			p.ARP.SenderIP, p.ARP.SenderMAC,
			p.ARP.TargetIP, p.ARP.TargetMAC)
	} else if saddr, daddr := l3Addrs(p.IPv4, p.IPv6); saddr != "" {
		// L4 with ports rewrites the addresses through JoinHostPort; L3-only
		// and ICMP keep the bare IPs.
		switch {
		case p.TCP != nil:
			saddr = net.JoinHostPort(saddr, strconv.Itoa(int(p.TCP.SrcPort)))
			daddr = net.JoinHostPort(daddr, strconv.Itoa(int(p.TCP.DstPort)))
		case p.UDP != nil:
			saddr = net.JoinHostPort(saddr, strconv.Itoa(int(p.UDP.SrcPort)))
			daddr = net.JoinHostPort(daddr, strconv.Itoa(int(p.UDP.DstPort)))
		}

		fmt.Fprintf(&b, " %s > %s", saddr, daddr)

		switch {
		case p.TCP != nil:
			fmt.Fprintf(&b, " [%s] seq=%d ack=%d win=%d",
				p.TCP.Flags, p.TCP.Seq, p.TCP.Ack, p.TCP.Window)

			if p.TCP.SkState != "" {
				fmt.Fprintf(&b, " sk=%s", p.TCP.SkState)
			}
		case p.UDP != nil:
			fmt.Fprintf(&b, " len=%d chk=0x%04x", p.UDP.Length, p.UDP.Checksum)
		case p.ICMP != nil:
			fmt.Fprintf(&b, " type=%s", p.ICMP.Type)

			if p.ICMP.ID != 0 || p.ICMP.Seq != 0 {
				fmt.Fprintf(&b, " id=%d seq=%d", p.ICMP.ID, p.ICMP.Seq)
			}
		}
	}

	return b.String()
}

func l3Addrs(v4 *IPv4, v6 *IPv6) (saddr, daddr string) {
	switch {
	case v4 != nil:
		return v4.Src.String(), v4.Dst.String()
	case v6 != nil:
		return v6.Src.String(), v6.Dst.String()
	}

	return "", ""
}

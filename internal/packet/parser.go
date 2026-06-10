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
	"net"
	"sync"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
)

const ethernetHeaderLen = 14

type parserState struct {
	eth   layers.Ethernet
	ipv4  layers.IPv4
	ipv6  layers.IPv6
	tcp   layers.TCP
	udp   layers.UDP
	icmp4 layers.ICMPv4
	icmp6 layers.ICMPv6
	arp   layers.ARP
	dlp   *gopacket.DecodingLayerParser
	lyrs  []gopacket.LayerType
	// frame holds the reconstructed Ethernet frame; reused across calls to
	// avoid a heap allocation on every packet in the hot path.
	frame [ethernetHeaderLen + RawCapacity]byte
}

var parserPool = sync.Pool{
	New: func() any {
		s := &parserState{lyrs: make([]gopacket.LayerType, 0, 5)}
		s.dlp = gopacket.NewDecodingLayerParser(
			layers.LayerTypeEthernet,
			&s.eth, &s.ipv4, &s.ipv6,
			&s.tcp, &s.udp, &s.icmp4, &s.icmp6, &s.arp,
		)
		s.dlp.IgnoreUnsupported = true

		return s
	},
}

// ParsePacketHdr decodes pkt into a PacketType and protocol-specific PacketInfo.
// When pkt.HasEthHdr == 1 the MAC addresses are embedded in the returned info struct.
func ParsePacketHdr(pkt *PacketHdr) (PacketType, PacketInfo) {
	ps := parserPool.Get().(*parserState)
	defer parserPool.Put(ps)

	rawLen := int(pkt.RawLen)
	if rawLen > len(pkt.Raw) {
		rawLen = len(pkt.Raw)
	}

	var frame []byte

	if pkt.HasEthHdr == 1 {
		frame = ps.frame[:rawLen]
		copy(frame, pkt.Raw[:rawLen])
	} else {
		frame = ps.frame[:ethernetHeaderLen+rawLen]
		binary.BigEndian.PutUint16(frame[12:], pkt.EthProto)
		copy(frame[ethernetHeaderLen:], pkt.Raw[:rawLen])
	}

	ps.lyrs = ps.lyrs[:0]

	if err := ps.dlp.DecodeLayers(frame, &ps.lyrs); err != nil && len(ps.lyrs) == 0 {
		return PacketTypeUnknown, nil
	}

	var srcMAC, dstMAC string

	if pkt.HasEthHdr == 1 && len(ps.eth.SrcMAC) == 6 {
		srcMAC = ps.eth.SrcMAC.String()
		dstMAC = ps.eth.DstMAC.String()
	}

	var hasIPv4, hasIPv6, hasTCP, hasUDP, hasICMP4, hasICMP6, hasARP bool

	for _, lt := range ps.lyrs {
		switch lt {
		case layers.LayerTypeIPv4:
			hasIPv4 = true
		case layers.LayerTypeIPv6:
			hasIPv6 = true
		case layers.LayerTypeTCP:
			hasTCP = true
		case layers.LayerTypeUDP:
			hasUDP = true
		case layers.LayerTypeICMPv4:
			hasICMP4 = true
		case layers.LayerTypeICMPv6:
			hasICMP6 = true
		case layers.LayerTypeARP:
			hasARP = true
		}
	}

	var pktType PacketType
	var info PacketInfo

	switch {
	case hasIPv4 && hasTCP:
		pktType = PacketTypeIPv4TCP
		info = &TCPInfo{
			SrcMAC:  srcMAC,
			DstMAC:  dstMAC,
			Saddr:   ps.ipv4.SrcIP.String(),
			Daddr:   ps.ipv4.DstIP.String(),
			Sport:   uint16(ps.tcp.SrcPort),
			Dport:   uint16(ps.tcp.DstPort),
			SkState: tcpStateName(pkt.SkState),
			Seq:     ps.tcp.Seq,
			AckSeq:  ps.tcp.Ack,
			Window:  ps.tcp.Window,
			Flags:   tcpFlags(&ps.tcp),
		}
	case hasIPv4 && hasUDP:
		pktType = PacketTypeIPv4UDP
		info = &UDPInfo{
			SrcMAC:   srcMAC,
			DstMAC:   dstMAC,
			Saddr:    ps.ipv4.SrcIP.String(),
			Daddr:    ps.ipv4.DstIP.String(),
			Sport:    uint16(ps.udp.SrcPort),
			Dport:    uint16(ps.udp.DstPort),
			Len:      ps.udp.Length,
			Checksum: ps.udp.Checksum,
		}
	case hasIPv4 && hasICMP4:
		pktType = PacketTypeIPv4ICMP
		info = &ICMPInfo{
			SrcMAC:   srcMAC,
			DstMAC:   dstMAC,
			Saddr:    ps.ipv4.SrcIP.String(),
			Daddr:    ps.ipv4.DstIP.String(),
			ICMPType: ps.icmp4.TypeCode.String(),
			ID:       ps.icmp4.Id,
			Seq:      ps.icmp4.Seq,
			Checksum: ps.icmp4.Checksum,
		}
	case hasIPv6 && hasTCP:
		pktType = PacketTypeIPv6TCP
		info = &TCPInfo{
			SrcMAC:  srcMAC,
			DstMAC:  dstMAC,
			Saddr:   ps.ipv6.SrcIP.String(),
			Daddr:   ps.ipv6.DstIP.String(),
			Sport:   uint16(ps.tcp.SrcPort),
			Dport:   uint16(ps.tcp.DstPort),
			SkState: tcpStateName(pkt.SkState),
			Seq:     ps.tcp.Seq,
			AckSeq:  ps.tcp.Ack,
			Window:  ps.tcp.Window,
			Flags:   tcpFlags(&ps.tcp),
		}
	case hasIPv6 && hasUDP:
		pktType = PacketTypeIPv6UDP
		info = &UDPInfo{
			SrcMAC: srcMAC,
			DstMAC: dstMAC,
			Saddr:  ps.ipv6.SrcIP.String(),
			Daddr:  ps.ipv6.DstIP.String(),
			Sport:  uint16(ps.udp.SrcPort),
			Dport:  uint16(ps.udp.DstPort),
			Len:    ps.udp.Length,
		}
	case hasIPv6 && hasICMP6:
		pktType = PacketTypeIPv6ICMPv6
		info = &ICMPInfo{
			SrcMAC:   srcMAC,
			DstMAC:   dstMAC,
			Saddr:    ps.ipv6.SrcIP.String(),
			Daddr:    ps.ipv6.DstIP.String(),
			ICMPType: ps.icmp6.TypeCode.String(),
		}
	case hasARP:
		op := "request"
		if ps.arp.Operation == 2 {
			op = "reply"
		}

		pktType = PacketTypeARP
		info = &ARPInfo{
			SrcMAC:    srcMAC,
			DstMAC:    dstMAC,
			Operation: op,
			SenderMAC: net.HardwareAddr(ps.arp.SourceHwAddress).String(),
			SenderIP:  net.IP(ps.arp.SourceProtAddress).String(),
			TargetMAC: net.HardwareAddr(ps.arp.DstHwAddress).String(),
			TargetIP:  net.IP(ps.arp.DstProtAddress).String(),
		}
	}

	return pktType, info
}

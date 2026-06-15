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
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
)

const ethernetHeaderLen = 14

// ErrNoLayers is returned by Parse when the frame yields no
// recognisable layers (truncated buffer or unsupported EtherType).
var ErrNoLayers = errors.New("packet: no layers decoded")

type decoder struct {
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
	// frame holds a synthesized Ethernet frame for the HasEthHdr==0 path,
	// reused across calls. The HasEthHdr==1 path slices pkt.Raw directly and
	// does not touch this buffer.
	frame [ethernetHeaderLen + RawCapacity]byte
}

var decoderPool = sync.Pool{
	New: func() any {
		dec := &decoder{lyrs: make([]gopacket.LayerType, 0, 5)}
		dec.dlp = gopacket.NewDecodingLayerParser(
			layers.LayerTypeEthernet,
			&dec.eth, &dec.ipv4, &dec.ipv6,
			&dec.tcp, &dec.udp, &dec.icmp4, &dec.icmp6, &dec.arp,
		)
		dec.dlp.IgnoreUnsupported = true

		return dec
	},
}

// Parse decodes pkt into a layered *Packet. The set of non-nil
// layer pointers reflects what was successfully parsed; callers compose
// behaviour against `p.TCP != nil`, `p.IPv4 != nil`, etc.
//
// Returns (nil, ErrNoLayers) when the buffer yields no recognisable layers.
// A decode error that still produced at least one layer returns the partial
// packet and a nil error — partial frames are useful for dropwatch tracing.
//
// Layer values are deep-copied out of the parser pool, so the returned
// *Packet is safe to retain after this call.
func Parse(pkt *Hdr) (*Packet, error) {
	dec := decoderPool.Get().(*decoder)
	defer decoderPool.Put(dec)

	rawLen := int(pkt.RawLen)
	if rawLen > len(pkt.Raw) {
		rawLen = len(pkt.Raw)
	}

	var frame []byte

	if pkt.HasEthHdr == 1 {
		// Slice pkt.Raw directly — DecodeLayers reads only, and every value we
		// extract below is either a primitive or a freshly-allocated copy, so
		// nothing returned holds a reference to the buffer.
		frame = pkt.Raw[:rawLen]
	} else {
		frame = dec.frame[:ethernetHeaderLen+rawLen]
		binary.BigEndian.PutUint16(frame[12:], pkt.EthProto)
		copy(frame[ethernetHeaderLen:], pkt.Raw[:rawLen])
	}

	dec.lyrs = dec.lyrs[:0]

	if err := dec.dlp.DecodeLayers(frame, &dec.lyrs); err != nil && len(dec.lyrs) == 0 {
		return nil, fmt.Errorf("packet: decode layers: %w", err)
	}

	if len(dec.lyrs) == 0 {
		return nil, ErrNoLayers
	}

	out := &Packet{}

	if pkt.HasEthHdr == 1 && len(dec.eth.SrcMAC) == 6 {
		out.Ether = &Ether{
			Src:    dec.eth.SrcMAC.String(),
			Dst:    dec.eth.DstMAC.String(),
			Type:   dec.eth.EthernetType.String(),
			Length: dec.eth.Length,
		}
	}

	for _, lt := range dec.lyrs {
		switch lt {
		case layers.LayerTypeIPv4:
			out.IPv4 = &IPv4{
				Version:    dec.ipv4.Version,
				IHL:        dec.ipv4.IHL,
				TOS:        dec.ipv4.TOS,
				Length:     dec.ipv4.Length,
				ID:         dec.ipv4.Id,
				Flags:      dec.ipv4.Flags.String(),
				FragOffset: dec.ipv4.FragOffset,
				TTL:        dec.ipv4.TTL,
				Protocol:   dec.ipv4.Protocol.String(),
				Checksum:   dec.ipv4.Checksum,
				Src:        slices.Clone(dec.ipv4.SrcIP),
				Dst:        slices.Clone(dec.ipv4.DstIP),
			}
		case layers.LayerTypeIPv6:
			out.IPv6 = &IPv6{
				Version:      dec.ipv6.Version,
				TrafficClass: dec.ipv6.TrafficClass,
				FlowLabel:    dec.ipv6.FlowLabel,
				Length:       dec.ipv6.Length,
				NextHeader:   dec.ipv6.NextHeader.String(),
				HopLimit:     dec.ipv6.HopLimit,
				Src:          slices.Clone(dec.ipv6.SrcIP),
				Dst:          slices.Clone(dec.ipv6.DstIP),
			}
		case layers.LayerTypeTCP:
			out.TCP = &TCP{
				SrcPort:    uint16(dec.tcp.SrcPort),
				DstPort:    uint16(dec.tcp.DstPort),
				Seq:        dec.tcp.Seq,
				Ack:        dec.tcp.Ack,
				DataOffset: dec.tcp.DataOffset,
				Flags:      tcpFlags(&dec.tcp),
				Window:     dec.tcp.Window,
				Checksum:   dec.tcp.Checksum,
				Urgent:     dec.tcp.Urgent,
				SkState:    tcpStateName(pkt.SkState),
			}
		case layers.LayerTypeUDP:
			out.UDP = &UDP{
				SrcPort:  uint16(dec.udp.SrcPort),
				DstPort:  uint16(dec.udp.DstPort),
				Length:   dec.udp.Length,
				Checksum: dec.udp.Checksum,
			}
		case layers.LayerTypeICMPv4:
			out.ICMP = &ICMP{
				Type:     dec.icmp4.TypeCode.String(),
				Code:     dec.icmp4.TypeCode.Code(),
				Checksum: dec.icmp4.Checksum,
				ID:       dec.icmp4.Id,
				Seq:      dec.icmp4.Seq,
			}
		case layers.LayerTypeICMPv6:
			out.ICMP = &ICMP{
				Type:     dec.icmp6.TypeCode.String(),
				Code:     dec.icmp6.TypeCode.Code(),
				Checksum: dec.icmp6.Checksum,
			}
		case layers.LayerTypeARP:
			out.ARP = &ARP{
				AddrType:        dec.arp.AddrType.String(),
				Protocol:        dec.arp.Protocol.String(),
				HwAddressSize:   dec.arp.HwAddressSize,
				ProtAddressSize: dec.arp.ProtAddressSize,
				Operation:       arpOperationName(dec.arp.Operation),
				SenderMAC:       net.HardwareAddr(dec.arp.SourceHwAddress).String(),
				SenderIP:        net.IP(slices.Clone(dec.arp.SourceProtAddress)),
				TargetMAC:       net.HardwareAddr(dec.arp.DstHwAddress).String(),
				TargetIP:        net.IP(slices.Clone(dec.arp.DstProtAddress)),
			}
		}
	}

	// A frame that only yielded our synthetic Ethernet wrapper (or whose
	// EtherType is unsupported) carries no usable protocol — treat it as
	// unparseable so callers can distinguish from a real partial parse.
	if out.IPv4 == nil && out.IPv6 == nil &&
		out.TCP == nil && out.UDP == nil &&
		out.ICMP == nil && out.ARP == nil {
		return nil, ErrNoLayers
	}

	return out, nil
}

// TCP flag bit positions used to index tcpFlagStrings. The bit assignment is
// internal — only the (ordered) string output is observable.
const (
	flagSYN uint8 = 1 << iota
	flagACK
	flagFIN
	flagRST
	flagPSH
	flagURG
	flagECE
	flagCWR
)

// tcpFlagStrings is a 256-entry lookup table from a packed flag byte to its
// "SYN|ACK"-style rendering. Building strings via strings.Builder per packet
// dominated the TCP hot path; precomputing all 2^8 combinations costs ~4 KB
// of static memory and turns tcpFlags into a zero-allocation indexed read.
var tcpFlagStrings [256]string

func init() {
	names := [...]struct {
		bit  uint8
		name string
	}{
		{flagSYN, "SYN"},
		{flagACK, "ACK"},
		{flagFIN, "FIN"},
		{flagRST, "RST"},
		{flagPSH, "PSH"},
		{flagURG, "URG"},
		{flagECE, "ECE"},
		{flagCWR, "CWR"},
	}

	for i := 0; i < 256; i++ {
		var parts []string

		for _, n := range names {
			if uint8(i)&n.bit != 0 {
				parts = append(parts, n.name)
			}
		}

		tcpFlagStrings[i] = strings.Join(parts, "|")
	}
}

func tcpFlags(tcp *layers.TCP) string {
	var b uint8

	if tcp.SYN {
		b |= flagSYN
	}

	if tcp.ACK {
		b |= flagACK
	}

	if tcp.FIN {
		b |= flagFIN
	}

	if tcp.RST {
		b |= flagRST
	}

	if tcp.PSH {
		b |= flagPSH
	}

	if tcp.URG {
		b |= flagURG
	}

	if tcp.ECE {
		b |= flagECE
	}

	if tcp.CWR {
		b |= flagCWR
	}

	return tcpFlagStrings[b]
}

var tcpStateNames = []string{
	"unknown",
	"ESTABLISHED",
	"SYN_SENT",
	"SYN_RECV",
	"FIN_WAIT1",
	"FIN_WAIT2",
	"TIME_WAIT",
	"CLOSE",
	"CLOSE_WAIT",
	"LAST_ACK",
	"LISTEN",
	"CLOSING",
	"NEW_SYN_RECV",
}

func tcpStateName(state uint8) string {
	if int(state) < len(tcpStateNames) {
		return tcpStateNames[state]
	}

	return fmt.Sprintf("UNKNOWN(%d)", state)
}

func arpOperationName(op uint16) string {
	switch op {
	case layers.ARPRequest:
		return "request"
	case layers.ARPReply:
		return "reply"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", op)
	}
}

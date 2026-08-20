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

package netcorrelate

import (
	"net"
	"net/netip"

	"huatuo-bamai/internal/packet"
	"huatuo-bamai/pkg/types"

	"golang.org/x/sys/unix"
)

const (
	tcpRetransmitSKBEventType    = "tcp_retransmit_skb"
	tcpRetransmitSYNACKEventType = "tcp_retransmit_synack"
)

// TODO: Keep SACK/DSACK, RST, ICMP, and TLP as unknown until their causal
// sequence rules and one stable cross-kernel socket identity are verified.

type retransMatchKind uint8

const (
	retransMatchUnsupported retransMatchKind = iota
	retransMatchData
	retransMatchSYN
	retransMatchSYNACK
)

type endpoint struct {
	addr [16]byte //nolint:unused // Participates in comparable flowKey values.
	port uint16   //nolint:unused // Participates in comparable flowKey values.
}

type flowKey struct {
	src    endpoint
	dst    endpoint
	family uint8
}

type namespaceID struct {
	cookie uint64
	inode  uint32
}

type dropEntry struct {
	event       *DropEvent
	flow        flowKey
	namespace   namespaceID
	ktimeNS     uint64
	seqStart    uint32
	seqEnd      uint32
	hasSeqRange bool
	ack         uint32
	ackFlag     bool
	synFlag     bool
	rstFlag     bool
	id          uint64
}

type retransEntry struct {
	flow        flowKey
	namespace   namespaceID
	ktimeNS     uint64
	seqStart    uint32
	seqEnd      uint32
	hasSeqRange bool
	ack         uint32
	kind        retransMatchKind
}

func parseDrop(event *DropEvent) (dropEntry, bool) {
	if event == nil || event.KtimeNS == 0 || event.Layers == nil ||
		event.Layers.TCP == nil ||
		(event.NetNamespaceCookie == 0 && event.NetNamespaceInode == 0) {
		return dropEntry{}, false
	}
	flow, ok := flowFromPacket(event.Layers)
	if !ok {
		return dropEntry{}, false
	}
	flags := event.Layers.TCP.RawFlags
	span, ok := tcpSequenceSpan(event, flags)
	if !ok {
		return dropEntry{}, false
	}
	return dropEntry{
		event:       event,
		flow:        flow,
		namespace:   namespaceID{cookie: event.NetNamespaceCookie, inode: event.NetNamespaceInode},
		ktimeNS:     event.KtimeNS,
		seqStart:    event.Layers.TCP.Seq,
		seqEnd:      event.Layers.TCP.Seq + span,
		hasSeqRange: span != 0,
		ack:         event.Layers.TCP.AckSeq,
		ackFlag:     flags&packet.TCPFlagACK != 0,
		synFlag:     flags&packet.TCPFlagSYN != 0,
		rstFlag:     flags&packet.TCPFlagRST != 0,
	}, true
}

func parseRetrans(event *types.TCPRetransmitTracing) (retransEntry, bool) {
	if event == nil || event.KtimeNS == 0 ||
		(event.NetNamespaceCookie == 0 && event.NetNamespaceInum == 0) {
		return retransEntry{}, false
	}
	src, family, ok := parseAddr(event.TCPSaddr)
	if !ok {
		return retransEntry{}, false
	}
	dst, dstFamily, ok := parseAddr(event.TCPDaddr)
	if !ok || family != dstFamily {
		return retransEntry{}, false
	}
	switch event.AddressFamily {
	case unix.AF_INET:
		if family != 4 {
			return retransEntry{}, false
		}
	case unix.AF_INET6:
		if family != 6 {
			return retransEntry{}, false
		}
	default:
		return retransEntry{}, false
	}
	flags := event.TCPFlagsRaw
	kind := retransMatchUnsupported
	switch event.EventType {
	case tcpRetransmitSYNACKEventType:
		if flags&packet.TCPFlagSYN != 0 && flags&packet.TCPFlagACK != 0 &&
			flags&packet.TCPFlagRST == 0 {
			kind = retransMatchSYNACK
		}
	case tcpRetransmitSKBEventType:
		switch {
		case flags&packet.TCPFlagSYN != 0 && flags&packet.TCPFlagACK == 0 &&
			flags&packet.TCPFlagRST == 0:
			kind = retransMatchSYN
		case flags&packet.TCPFlagSYN == 0 && flags&packet.TCPFlagACK != 0 &&
			flags&packet.TCPFlagRST == 0:
			kind = retransMatchData
		}
	}
	hasEndSeq := event.EventType == tcpRetransmitSKBEventType ||
		event.EventType == tcpRetransmitSYNACKEventType
	hasRange := hasEndSeq && tcpSeqBefore(event.TCPSeq, event.TCPEndSeq)
	return retransEntry{
		flow: flowKey{
			src:    endpoint{addr: src, port: event.TCPSport},
			dst:    endpoint{addr: dst, port: event.TCPDport},
			family: family,
		},
		namespace:   namespaceID{cookie: event.NetNamespaceCookie, inode: event.NetNamespaceInum},
		ktimeNS:     event.KtimeNS,
		seqStart:    event.TCPSeq,
		seqEnd:      event.TCPEndSeq,
		hasSeqRange: hasRange,
		ack:         event.TCPAckSeq,
		kind:        kind,
	}, true
}

func flowFromPacket(layers *packet.Packet) (flowKey, bool) {
	if layers == nil || layers.TCP == nil {
		return flowKey{}, false
	}
	var src, dst [16]byte
	var family uint8
	switch {
	case layers.IPv4 != nil:
		var ok bool
		src, ok = ipv4Array(layers.IPv4.Saddr)
		if !ok {
			return flowKey{}, false
		}
		dst, ok = ipv4Array(layers.IPv4.Daddr)
		if !ok {
			return flowKey{}, false
		}
		family = 4
	case layers.IPv6 != nil:
		var ok bool
		src, ok = ipv6Array(layers.IPv6.Saddr)
		if !ok {
			return flowKey{}, false
		}
		dst, ok = ipv6Array(layers.IPv6.Daddr)
		if !ok {
			return flowKey{}, false
		}
		family = 6
	default:
		return flowKey{}, false
	}
	return flowKey{
		src:    endpoint{addr: src, port: layers.TCP.Sport},
		dst:    endpoint{addr: dst, port: layers.TCP.Dport},
		family: family,
	}, true
}

func parseAddr(value string) ([16]byte, uint8, bool) {
	addr, err := netip.ParseAddr(value)
	if err != nil {
		return [16]byte{}, 0, false
	}
	addr = addr.Unmap()
	switch {
	case addr.Is4():
		var out [16]byte
		v4 := addr.As4()
		copy(out[:4], v4[:])
		return out, 4, true
	case addr.Is6():
		return addr.As16(), 6, true
	default:
		return [16]byte{}, 0, false
	}
}

func ipv4Array(ip net.IP) ([16]byte, bool) {
	v4 := ip.To4()
	if v4 == nil {
		return [16]byte{}, false
	}
	var out [16]byte
	copy(out[:4], v4)
	return out, true
}

func ipv6Array(ip net.IP) ([16]byte, bool) {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok || !addr.Is6() || addr.Is4In6() {
		return [16]byte{}, false
	}
	return addr.As16(), true
}

func tcpSequenceSpan(event *DropEvent, flags uint8) (uint32, bool) {
	if event == nil || event.Layers == nil || event.Layers.TCP == nil {
		return 0, false
	}
	payload, ok := tcpPayloadLen(event)
	if !ok {
		return 0, false
	}
	if flags&packet.TCPFlagSYN != 0 {
		payload++
	}
	if flags&packet.TCPFlagFIN != 0 {
		payload++
	}
	return payload, true
}

func tcpPayloadLen(event *DropEvent) (uint32, bool) {
	layers := event.Layers
	tcpHeaderLen := uint32(layers.TCP.DataOffset) * 4
	switch {
	case layers.IPv4 != nil:
		ipHeaderLen := uint32(layers.IPv4.IHL) * 4
		totalLen := uint32(layers.IPv4.Length)
		if event.PacketLen > totalLen {
			totalLen = event.PacketLen
		}
		if ipHeaderLen+tcpHeaderLen > totalLen {
			return 0, false
		}
		return totalLen - ipHeaderLen - tcpHeaderLen, true
	case layers.IPv6 != nil:
		if layers.IPv6.NextHeader != "TCP" {
			return 0, false
		}
		payloadLen := uint32(layers.IPv6.Length)
		if event.PacketLen > 40+payloadLen {
			payloadLen = event.PacketLen - 40
		}
		if tcpHeaderLen > payloadLen {
			return 0, false
		}
		return payloadLen - tcpHeaderLen, true
	default:
		return 0, false
	}
}

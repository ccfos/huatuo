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

package main

import (
	"net"
	"net/netip"
	"time"

	"github.com/ccfos/huatuo/internal/packet"
	"github.com/ccfos/huatuo/internal/symbol"
	"github.com/ccfos/huatuo/pkg/types"
)

const (
	tcpRetransmitSKBEventType    = "tcp_retransmit_skb"
	tcpRetransmitSYNACKEventType = "tcp_retransmit_synack"
	maxDropToRetransmitAge       = time.Second
)

type retransmitMatchKind uint8

const (
	retransmitMatchUnsupported retransmitMatchKind = iota
	retransmitMatchData
	retransmitMatchSYN
	retransmitMatchSYNACK
)

type endpoint struct {
	address netip.Addr
	port    uint16
}

type flowKey struct {
	source      endpoint
	destination endpoint
}

type namespaceID struct {
	cookie uint64
	inode  uint32
}

type dropEvent struct {
	ktimeNS   uint64
	namespace namespaceID

	flow        flowKey
	sequence    uint32
	endSequence uint32
	ackSequence uint32
	tcpFlags    uint8

	stackDepth uint8
	stackPCs   [symbol.KsymStackMaxDepth]uint64
}

type retransmitEntry struct {
	flow        flowKey
	namespace   namespaceID
	ktimeNS     uint64
	sequence    uint32
	endSequence uint32
	hasSequence bool
	ackSequence uint32
	kind        retransmitMatchKind
}

func retransmitEntryFromEvent(event *types.TCPRetransmitTracing) (retransmitEntry, bool) {
	hasNamespace := event != nil &&
		(event.NetNamespaceCookie != 0 || event.NetNamespaceInum != 0)
	if event == nil || event.KtimeNS == 0 || !hasNamespace {
		return retransmitEntry{}, false
	}

	source, ok := parseAddress(event.TCPSaddr)
	if !ok {
		return retransmitEntry{}, false
	}
	destination, ok := parseAddress(event.TCPDaddr)
	if !ok || source.Is4() != destination.Is4() {
		return retransmitEntry{}, false
	}

	hasEndSequence := event.EventType == tcpRetransmitSKBEventType ||
		event.EventType == tcpRetransmitSYNACKEventType
	hasSequence := hasEndSequence && tcpSequenceBefore(event.TCPSeq, event.TCPEndSeq)

	return retransmitEntry{
		flow: flowKey{
			source: endpoint{
				address: source,
				port:    event.TCPSport,
			},
			destination: endpoint{
				address: destination,
				port:    event.TCPDport,
			},
		},
		namespace: namespaceID{
			cookie: event.NetNamespaceCookie,
			inode:  event.NetNamespaceInum,
		},
		ktimeNS:     event.KtimeNS,
		sequence:    event.TCPSeq,
		endSequence: event.TCPEndSeq,
		hasSequence: hasSequence,
		ackSequence: event.TCPAckSeq,
		kind:        retransmitMatchKindFromEvent(event),
	}, true
}

func retransmitMatchKindFromEvent(event *types.TCPRetransmitTracing) retransmitMatchKind {
	flags := event.TCPFlagsRaw
	hasSYN := flags&packet.TCPFlagSYN != 0
	hasACK := flags&packet.TCPFlagACK != 0
	hasRST := flags&packet.TCPFlagRST != 0

	switch event.EventType {
	case tcpRetransmitSYNACKEventType:
		if hasSYN && hasACK && !hasRST {
			return retransmitMatchSYNACK
		}
	case tcpRetransmitSKBEventType:
		switch {
		case hasSYN && !hasACK && !hasRST:
			return retransmitMatchSYN
		case !hasSYN && hasACK && !hasRST:
			return retransmitMatchData
		}
	}
	return retransmitMatchUnsupported
}

func flowFromPacket(layers *packet.Packet) (flowKey, bool) {
	if layers == nil || layers.TCP == nil {
		return flowKey{}, false
	}

	var source, destination netip.Addr
	switch {
	case layers.IPv4 != nil:
		var ok bool
		source, ok = addressFromIP(layers.IPv4.Saddr)
		if !ok || !source.Is4() {
			return flowKey{}, false
		}
		destination, ok = addressFromIP(layers.IPv4.Daddr)
		if !ok || !destination.Is4() {
			return flowKey{}, false
		}
	case layers.IPv6 != nil:
		var ok bool
		source, ok = addressFromIP(layers.IPv6.Saddr)
		if !ok || !source.Is6() {
			return flowKey{}, false
		}
		destination, ok = addressFromIP(layers.IPv6.Daddr)
		if !ok || !destination.Is6() {
			return flowKey{}, false
		}
	default:
		return flowKey{}, false
	}

	return flowKey{
		source: endpoint{
			address: source,
			port:    layers.TCP.Sport,
		},
		destination: endpoint{
			address: destination,
			port:    layers.TCP.Dport,
		},
	}, true
}

func reverseFlow(flow flowKey) flowKey {
	return flowKey{
		source: endpoint{
			address: flow.destination.address,
			port:    flow.destination.port,
		},
		destination: endpoint{
			address: flow.source.address,
			port:    flow.source.port,
		},
	}
}

func parseAddress(value string) (netip.Addr, bool) {
	address, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}

func addressFromIP(ip net.IP) (netip.Addr, bool) {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return netip.Addr{}, false
	}
	return address.Unmap(), true
}

func dropMatchesRetransmit(drop *dropEvent, retransmit *retransmitEntry) bool {
	if !dropWithinRetransmitAge(drop, retransmit) {
		return false
	}
	if drop.flow == retransmit.flow && outboundSegmentMatches(drop, retransmit) {
		return true
	}
	return drop.flow == reverseFlow(retransmit.flow) &&
		inboundACKMatches(drop, retransmit)
}

func dropWithinRetransmitAge(drop *dropEvent, retransmit *retransmitEntry) bool {
	if drop.ktimeNS > retransmit.ktimeNS {
		return false
	}
	ageNS := retransmit.ktimeNS - drop.ktimeNS
	return ageNS <= uint64(maxDropToRetransmitAge)
}

func outboundSegmentMatches(drop *dropEvent, retransmit *retransmitEntry) bool {
	return drop.tcpFlags&packet.TCPFlagRST == 0 &&
		retransmit.kind != retransmitMatchUnsupported &&
		drop.sequence != drop.endSequence && retransmit.hasSequence &&
		tcpSequenceRangesOverlap(
			drop.sequence,
			drop.endSequence,
			retransmit.sequence,
			retransmit.endSequence,
		)
}

func inboundACKMatches(drop *dropEvent, retransmit *retransmitEntry) bool {
	if drop.tcpFlags&packet.TCPFlagACK == 0 ||
		drop.tcpFlags&packet.TCPFlagRST != 0 ||
		!retransmit.hasSequence {
		return false
	}

	switch retransmit.kind {
	case retransmitMatchSYN:
		return drop.tcpFlags&packet.TCPFlagSYN != 0 &&
			drop.ackSequence == retransmit.endSequence
	case retransmitMatchSYNACK:
		return drop.tcpFlags&packet.TCPFlagSYN == 0 &&
			drop.ackSequence == retransmit.endSequence &&
			drop.sequence == retransmit.ackSequence
	case retransmitMatchData:
		return drop.tcpFlags&packet.TCPFlagSYN == 0 &&
			tcpSequenceBefore(retransmit.sequence, drop.ackSequence) &&
			!tcpSequenceBefore(drop.ackSequence, retransmit.endSequence)
	default:
		return false
	}
}

func sameNamespace(first, second namespaceID) bool {
	if first.cookie != 0 && second.cookie != 0 {
		return first.cookie == second.cookie
	}
	return first.inode != 0 && second.inode != 0 && first.inode == second.inode
}

// TCP sequence ordering is signed modulo 2^32. Ordinary uint32 comparison
// fails when a range crosses the wrap point; compared values must be less than
// 2^31 apart as required by RFC 1982 serial number arithmetic.
func tcpSequenceBefore(first, second uint32) bool {
	return int32(first-second) < 0
}

func tcpSequenceRangesOverlap(
	firstStart,
	firstEnd,
	secondStart,
	secondEnd uint32,
) bool {
	return tcpSequenceBefore(firstStart, secondEnd) &&
		tcpSequenceBefore(secondStart, firstEnd)
}

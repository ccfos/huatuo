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
	"math"
	"net"
	"testing"

	"golang.org/x/sys/unix"

	"huatuo-bamai/internal/packet"
	"huatuo-bamai/pkg/types"
)

func TestMatcherOutboundSegments(t *testing.T) {
	tests := []struct {
		name      string
		flags     uint8
		packetLen uint32
		seq       uint32
		end       uint32
	}{
		{name: "syn", flags: packet.TCPFlagSYN, packetLen: 40, seq: 100, end: 101},
		{name: "data", flags: packet.TCPFlagACK | packet.TCPFlagPSH, packetLen: 140, seq: 100, end: 200},
		{name: "fin", flags: packet.TCPFlagACK | packet.TCPFlagFIN, packetLen: 40, seq: 100, end: 101},
		{name: "gso", flags: packet.TCPFlagACK, packetLen: 4040, seq: 100, end: 4100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMatcher()
			drop := testDrop(10, "10.0.0.1", "10.0.0.2", 1000, 80, tt.seq, 0, tt.flags, tt.packetLen)
			if !m.add(drop) {
				t.Fatal("add = false")
			}
			got := m.match(testRetrans(20, "10.0.0.1", "10.0.0.2", 1000, 80, tt.seq, tt.end))
			if got != drop {
				t.Fatalf("match = %p, want %p", got, drop)
			}
		})
	}
}

func TestMatcherUsesRawTCPFlags(t *testing.T) {
	m := newMatcher()
	drop := testDrop(
		10, "10.0.0.1", "10.0.0.2", 1000, 80,
		100, 0, packet.TCPFlagACK, 140,
	)
	drop.Layers.TCP.Flags = "RST"
	if !m.add(drop) {
		t.Fatal("add = false")
	}

	retrans := testRetrans(
		20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200,
	)
	retrans.TCPFlags = "RST"
	if got := m.match(retrans); got != drop {
		t.Fatalf("match = %p, want %p", got, drop)
	}
}

func TestMatcherAddReleasesNormalizedPacket(t *testing.T) {
	m := newMatcher()
	drop := testDrop(10, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 0, packet.TCPFlagACK, 140)
	drop.StackDepth = 1
	drop.StackPCs[0] = 0x1234

	if !m.add(drop) {
		t.Fatal("add = false")
	}
	if drop.Layers != nil {
		t.Fatalf("Layers = %+v, want released packet", drop.Layers)
	}
	if drop.StackDepth != 1 || drop.StackPCs[0] != 0x1234 {
		t.Fatalf("stack = depth %d PC %#x, want retained stack", drop.StackDepth, drop.StackPCs[0])
	}
	if got := m.match(testRetrans(20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)); got != drop {
		t.Fatalf("match = %p, want %p", got, drop)
	}
}

func TestMatcherAddDoesNotMutateRejectedEvent(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DropEvent)
	}{
		{
			name: "missing ktime",
			mutate: func(drop *DropEvent) {
				drop.KtimeNS = 0
			},
		},
		{
			name: "missing namespace",
			mutate: func(drop *DropEvent) {
				drop.NetNamespaceCookie = 0
				drop.NetNamespaceInode = 0
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMatcher()
			drop := testDrop(10, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 0, packet.TCPFlagACK, 140)
			layers := drop.Layers
			tt.mutate(drop)

			if m.add(drop) {
				t.Fatal("add = true")
			}
			if drop.Layers != layers {
				t.Fatalf("Layers = %p, want original %p", drop.Layers, layers)
			}
		})
	}
}

func TestMatcherCanonicalL3PacketLength(t *testing.T) {
	tests := []struct {
		name           string
		ipv6           bool
		packetLen      uint32
		ipv4TotalLen   uint16
		ipv6PayloadLen uint16
		wantEnd        uint32
	}{
		{
			name:         "ipv4",
			packetLen:    140,
			ipv4TotalLen: 140,
			wantEnd:      200,
		},
		{
			name:           "ipv6",
			ipv6:           true,
			packetLen:      160,
			ipv6PayloadLen: 120,
			wantEnd:        200,
		},
		{
			name:         "ipv4_gso",
			packetLen:    4040,
			ipv4TotalLen: 40,
			wantEnd:      4100,
		},
		{
			name:           "ipv6_gso",
			ipv6:           true,
			packetLen:      4060,
			ipv6PayloadLen: 20,
			wantEnd:        4100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const seq = 100
			layers := &packet.Packet{
				Label: "TCP",
				TCP: &packet.TCP{
					Sport: 1000, Dport: 80, Seq: seq,
					DataOffset: 5, Flags: packet.TCPFlagStrings[packet.TCPFlagACK|packet.TCPFlagPSH],
					RawFlags: packet.TCPFlagACK | packet.TCPFlagPSH,
				},
			}
			saddr, daddr := "10.0.0.1", "10.0.0.2"
			if tt.ipv6 {
				saddr, daddr = "2001:db8::1", "2001:db8::2"
				layers.IPv6 = &packet.IPv6{
					Version: 6, Length: tt.ipv6PayloadLen, NextHeader: "TCP",
					Saddr: net.ParseIP(saddr), Daddr: net.ParseIP(daddr),
				}
			} else {
				layers.IPv4 = &packet.IPv4{
					Version: 4, IHL: 5, Length: tt.ipv4TotalLen, Protocol: "TCP",
					Saddr: net.ParseIP(saddr), Daddr: net.ParseIP(daddr),
				}
			}

			drop := &DropEvent{
				KtimeNS: 10, NetNamespaceCookie: 1, NetNamespaceInode: 2,
				PacketLen: tt.packetLen, Layers: layers,
			}
			m := newMatcher()
			if !m.add(drop) {
				t.Fatal("add = false")
			}
			retrans := testRetrans(
				20,
				saddr,
				daddr,
				1000,
				80,
				seq,
				tt.wantEnd,
			)
			if got := m.match(retrans); got != drop {
				t.Fatalf("match = %p, want %p", got, drop)
			}
		})
	}
}

func TestMatcherInboundACKs(t *testing.T) {
	tests := []struct {
		name         string
		dropFlags    uint8
		dropACK      uint32
		retransFlags uint8
		retransType  string
		retransACK   uint32
		retransEnd   uint32
	}{
		{
			name:         "synack",
			dropFlags:    packet.TCPFlagSYN | packet.TCPFlagACK,
			dropACK:      101,
			retransFlags: packet.TCPFlagSYN,
			retransType:  tcpRetransmitSKBEventType,
			retransEnd:   101,
		},
		{
			name:         "final_ack",
			dropFlags:    packet.TCPFlagACK,
			dropACK:      101,
			retransFlags: packet.TCPFlagSYN | packet.TCPFlagACK,
			retransType:  tcpRetransmitSYNACKEventType,
			retransACK:   500,
			retransEnd:   101,
		},
		{
			name:         "data_ack",
			dropFlags:    packet.TCPFlagACK,
			dropACK:      200,
			retransFlags: packet.TCPFlagACK | packet.TCPFlagPSH,
			retransType:  tcpRetransmitSKBEventType,
			retransACK:   500,
			retransEnd:   200,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMatcher()
			drop := testDrop(
				10,
				"10.0.0.2",
				"10.0.0.1",
				80,
				1000,
				500,
				tt.dropACK,
				tt.dropFlags,
				40,
			)
			if !m.add(drop) {
				t.Fatal("add = false")
			}
			retrans := testRetrans(
				20,
				"10.0.0.1",
				"10.0.0.2",
				1000,
				80,
				100,
				tt.retransEnd,
			)
			retrans.TCPFlags = packet.TCPFlagStrings[tt.retransFlags]
			retrans.TCPFlagsRaw = tt.retransFlags
			retrans.EventType = tt.retransType
			retrans.TCPAckSeq = tt.retransACK
			got := m.match(retrans)
			if got != drop {
				t.Fatalf("match = %p, want %p", got, drop)
			}
		})
	}
}

func TestMatcherChoosesLatestCandidateAcrossDirections(t *testing.T) {
	tests := []struct {
		name          string
		outboundKtime uint64
		inboundKtime  uint64
		wantInbound   bool
	}{
		{
			name:          "newer inbound",
			outboundKtime: 10,
			inboundKtime:  20,
			wantInbound:   true,
		},
		{
			name:          "newer outbound",
			outboundKtime: 20,
			inboundKtime:  10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMatcher()
			outbound := testDrop(
				tt.outboundKtime,
				"10.0.0.1",
				"10.0.0.2",
				1000,
				80,
				100,
				0,
				packet.TCPFlagACK,
				140,
			)
			inbound := testDrop(
				tt.inboundKtime,
				"10.0.0.2",
				"10.0.0.1",
				80,
				1000,
				500,
				200,
				packet.TCPFlagACK,
				40,
			)
			if !m.add(outbound) || !m.add(inbound) {
				t.Fatal("add = false")
			}

			want, remaining := outbound, inbound
			if tt.wantInbound {
				want, remaining = inbound, outbound
			}
			retrans := testRetrans(
				30,
				"10.0.0.1",
				"10.0.0.2",
				1000,
				80,
				100,
				200,
			)
			if got := m.match(retrans); got != want {
				t.Fatalf("first match = %p, want %p", got, want)
			}
			if got := m.match(retrans); got != remaining {
				t.Fatalf("second match = %p, want %p", got, remaining)
			}
		})
	}
}

func TestMatcherBreaksKtimeTieByHigherID(t *testing.T) {
	m := newMatcher()
	first := testDrop(
		10,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		100,
		0,
		packet.TCPFlagACK,
		140,
	)
	second := testDrop(
		10,
		"10.0.0.2",
		"10.0.0.1",
		80,
		1000,
		500,
		200,
		packet.TCPFlagACK,
		40,
	)
	if !m.add(first) || !m.add(second) {
		t.Fatal("add = false")
	}

	retrans := testRetrans(
		20,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		100,
		200,
	)
	if got := m.match(retrans); got != second {
		t.Fatalf("first match = %p, want later entry %p", got, second)
	}
	if got := m.match(retrans); got != first {
		t.Fatalf("second match = %p, want earlier entry %p", got, first)
	}
}

func TestMatcherDoesNotConsumeCrossNetNSCandidate(t *testing.T) {
	m := newMatcher()
	drop := testDrop(
		10,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		100,
		0,
		packet.TCPFlagACK,
		140,
	)
	if !m.add(drop) {
		t.Fatal("add = false")
	}

	retrans := testRetrans(
		20,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		100,
		200,
	)
	retrans.NetNamespaceCookie++
	matched, hasCrossNetNSCandidate := m.matchWithCoverage(retrans)
	if matched != nil || !hasCrossNetNSCandidate {
		t.Fatalf(
			"match = %p, cross-netns candidate = %t; want nil, true",
			matched,
			hasCrossNetNSCandidate,
		)
	}
	if m.active != 1 || len(m.live) != 1 {
		t.Fatalf("state after rejected match = active %d, live %d; want 1, 1", m.active, len(m.live))
	}

	retrans.NetNamespaceCookie--
	matched, hasCrossNetNSCandidate = m.matchWithCoverage(retrans)
	if matched != drop || hasCrossNetNSCandidate {
		t.Fatalf(
			"match = %p, cross-netns candidate = %t; want %p, false",
			matched,
			hasCrossNetNSCandidate,
			drop,
		)
	}
	if m.active != 0 || len(m.live) != 0 || len(m.entries) != 0 {
		t.Fatalf(
			"state after match = active %d, live %d, buckets %d; want 0, 0, 0",
			m.active,
			len(m.live),
			len(m.entries),
		)
	}
}

func TestMatcherMatchRemovalKeepsStateConsistent(t *testing.T) {
	m := newMatcher()
	m.capacity = 3
	m.order = make([]orderRef, m.capacity)
	m.scratch = make([]orderRef, m.capacity)
	first := testDrop(10, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 0, packet.TCPFlagACK, 140)
	middle := testDrop(11, "10.0.0.1", "10.0.0.2", 1000, 80, 300, 0, packet.TCPFlagACK, 140)
	last := testDrop(12, "10.0.0.1", "10.0.0.2", 1000, 80, 500, 0, packet.TCPFlagACK, 140)
	if !m.add(first) || !m.add(middle) || !m.add(last) {
		t.Fatal("add = false")
	}
	key := m.order[0].flow
	firstID := m.entries[key][0].id
	middleID := m.entries[key][1].id

	if got := m.match(testRetrans(
		20,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		300,
		400,
	)); got != middle {
		t.Fatalf("match = %p, want %p", got, middle)
	}
	if m.active != 2 || len(m.live) != 2 {
		t.Fatalf("state after match = active %d, live %d; want 2, 2", m.active, len(m.live))
	}
	if m.used != 3 || m.head != 0 || m.order[1].id != middleID {
		t.Fatalf(
			"order after match = used %d, head %d, matched ID %d; want 3, 0, %d",
			m.used,
			m.head,
			m.order[1].id,
			middleID,
		)
	}
	if _, ok := m.live[middleID]; ok {
		t.Fatal("matched ID remains live")
	}
	entries := m.entries[key]
	if len(entries) != 2 || entries[0].event != first || entries[1].event != last {
		t.Fatalf("entries after match = %+v, want first and last", entries)
	}

	fourth := testDrop(13, "10.0.0.1", "10.0.0.2", 1000, 80, 700, 0, packet.TCPFlagACK, 140)
	if !m.add(fourth) {
		t.Fatal("add fourth = false")
	}
	if m.active != 3 || m.used != 3 || len(m.live) != 3 {
		t.Fatalf(
			"state after compaction = active %d, used %d, live %d; want 3, 3, 3",
			m.active,
			m.used,
			len(m.live),
		)
	}
	if m.evicted {
		t.Fatal("tombstone compaction marked live evidence as evicted")
	}

	fifth := testDrop(14, "10.0.0.1", "10.0.0.2", 1000, 80, 900, 0, packet.TCPFlagACK, 140)
	if !m.add(fifth) {
		t.Fatal("add fifth = false")
	}
	if _, ok := m.live[firstID]; ok {
		t.Fatal("evicted ID remains live")
	}
	entries = m.entries[key]
	if m.active != 3 || len(m.live) != 3 || len(entries) != 3 {
		t.Fatalf(
			"state after eviction = active %d, live %d, entries %d; want 3, 3, 3",
			m.active,
			len(m.live),
			len(entries),
		)
	}
	if entries[0].event != last || entries[1].event != fourth || entries[2].event != fifth {
		t.Fatalf("entries after eviction = %+v, want last, fourth, and fifth", entries)
	}
}

func TestMatcherRejectsWeakInboundACKEvidence(t *testing.T) {
	tests := []struct {
		name             string
		dropSeq          uint32
		dropACK          uint32
		dropFlags        uint8
		retransACK       uint32
		retransEnd       uint32
		retransFlags     uint8
		retransEventType string
	}{
		{
			name:             "syn_requires_synack",
			dropSeq:          500,
			dropACK:          101,
			dropFlags:        packet.TCPFlagACK,
			retransEnd:       101,
			retransFlags:     packet.TCPFlagSYN,
			retransEventType: tcpRetransmitSKBEventType,
		},
		{
			name:             "synack_ack_must_equal_syn_end",
			dropSeq:          500,
			dropACK:          102,
			dropFlags:        packet.TCPFlagSYN | packet.TCPFlagACK,
			retransEnd:       101,
			retransFlags:     packet.TCPFlagSYN,
			retransEventType: tcpRetransmitSKBEventType,
		},
		{
			name:             "final_ack_requires_request_sequence",
			dropSeq:          501,
			dropACK:          101,
			dropFlags:        packet.TCPFlagACK,
			retransACK:       500,
			retransEnd:       101,
			retransFlags:     packet.TCPFlagSYN | packet.TCPFlagACK,
			retransEventType: tcpRetransmitSYNACKEventType,
		},
		{
			name:             "final_ack_must_not_be_synack",
			dropSeq:          500,
			dropACK:          101,
			dropFlags:        packet.TCPFlagSYN | packet.TCPFlagACK,
			retransACK:       500,
			retransEnd:       101,
			retransFlags:     packet.TCPFlagSYN | packet.TCPFlagACK,
			retransEventType: tcpRetransmitSYNACKEventType,
		},
		{
			name:             "data_rejects_rst_ack",
			dropSeq:          500,
			dropACK:          200,
			dropFlags:        packet.TCPFlagRST | packet.TCPFlagACK,
			retransEnd:       200,
			retransFlags:     packet.TCPFlagACK | packet.TCPFlagPSH,
			retransEventType: tcpRetransmitSKBEventType,
		},
		{
			name:             "unknown_retrans_type",
			dropSeq:          500,
			dropACK:          200,
			dropFlags:        packet.TCPFlagACK,
			retransEnd:       200,
			retransFlags:     packet.TCPFlagACK,
			retransEventType: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMatcher()
			drop := testDrop(
				10,
				"10.0.0.2",
				"10.0.0.1",
				80,
				1000,
				tt.dropSeq,
				tt.dropACK,
				tt.dropFlags,
				40,
			)
			if !m.add(drop) {
				t.Fatal("add = false")
			}
			retrans := testRetrans(
				20,
				"10.0.0.1",
				"10.0.0.2",
				1000,
				80,
				100,
				tt.retransEnd,
			)
			retrans.TCPAckSeq = tt.retransACK
			retrans.TCPFlags = packet.TCPFlagStrings[tt.retransFlags]
			retrans.TCPFlagsRaw = tt.retransFlags
			retrans.EventType = tt.retransEventType
			if got := m.match(retrans); got != nil {
				t.Fatalf("match = %p, want nil", got)
			}
		})
	}
}

func TestMatcherRejectsWeakEvidence(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DropEvent, *types.TCPRetransmitTracing)
	}{
		{
			name: "wrong namespace",
			mutate: func(_ *DropEvent, retrans *types.TCPRetransmitTracing) {
				retrans.NetNamespaceCookie++
			},
		},
		{
			name: "wrong tuple",
			mutate: func(_ *DropEvent, retrans *types.TCPRetransmitTracing) {
				retrans.TCPDport++
			},
		},
		{
			name: "wrong sequence",
			mutate: func(_ *DropEvent, retrans *types.TCPRetransmitTracing) {
				retrans.TCPSeq, retrans.TCPEndSeq = 1000, 1100
			},
		},
		{
			name: "tuple only",
			mutate: func(_ *DropEvent, retrans *types.TCPRetransmitTracing) {
				retrans.TCPEndSeq = 0
			},
		},
		{
			name: "drop after retrans",
			mutate: func(drop *DropEvent, _ *types.TCPRetransmitTracing) {
				drop.KtimeNS = 30
			},
		},
		{
			name: "RST drop",
			mutate: func(drop *DropEvent, _ *types.TCPRetransmitTracing) {
				drop.Layers.TCP.RawFlags = packet.TCPFlagRST | packet.TCPFlagACK
			},
		},
		{
			name: "declared family mismatch",
			mutate: func(_ *DropEvent, retrans *types.TCPRetransmitTracing) {
				retrans.AddressFamily = unix.AF_INET6
			},
		},
		{
			name: "unsupported retransmission",
			mutate: func(_ *DropEvent, retrans *types.TCPRetransmitTracing) {
				retrans.EventType = "unknown"
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newMatcher()
			drop := testDrop(10, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 0, packet.TCPFlagACK, 140)
			retrans := testRetrans(20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)
			tt.mutate(drop, retrans)
			m.add(drop)
			if got := m.match(retrans); got != nil {
				t.Fatalf("match = %+v, want nil", got)
			}
		})
	}
}

func TestMatcherSequenceWrap(t *testing.T) {
	m := newMatcher()
	drop := testDrop(
		10, "10.0.0.1", "10.0.0.2", 1000, 80,
		math.MaxUint32-20, 0, packet.TCPFlagACK, 80,
	)
	m.add(drop)
	got := m.match(testRetrans(20, "10.0.0.1", "10.0.0.2", 1000, 80, math.MaxUint32-10, 20))
	if got != drop {
		t.Fatalf("match = %p, want %p", got, drop)
	}
}

func TestMatcherSequenceWrapsExactlyToZero(t *testing.T) {
	m := newMatcher()
	drop := testDrop(
		10,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		math.MaxUint32,
		0,
		packet.TCPFlagSYN,
		40,
	)
	if !m.add(drop) {
		t.Fatal("add = false")
	}
	retrans := testRetrans(
		20,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		math.MaxUint32,
		0,
	)
	retrans.TCPFlags = packet.TCPFlagStrings[packet.TCPFlagSYN]
	retrans.TCPFlagsRaw = packet.TCPFlagSYN
	if got := m.match(retrans); got != drop {
		t.Fatalf("match = %p, want %p", got, drop)
	}
}

func TestMatcherRetainsEntriesAfterMatch(t *testing.T) {
	m := newMatcher()
	first := testDrop(10, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 0, packet.TCPFlagACK, 140)
	second := testDrop(11, "10.0.0.1", "10.0.0.2", 1000, 80, 300, 0, packet.TCPFlagACK, 140)
	m.add(first)
	m.add(second)
	if got := m.match(testRetrans(20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)); got != first {
		t.Fatalf("first match = %p, want %p", got, first)
	}
	if got := m.match(testRetrans(21, "10.0.0.1", "10.0.0.2", 1000, 80, 300, 400)); got != second {
		t.Fatalf("second match = %p, want %p", got, second)
	}
}

func TestMatcherEvictsOldestAtCapacity(t *testing.T) {
	m := newMatcher()
	m.capacity = 2
	m.order = make([]orderRef, m.capacity)
	m.scratch = make([]orderRef, m.capacity)
	first := testDrop(10, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 0, packet.TCPFlagACK, 140)
	second := testDrop(11, "10.0.0.1", "10.0.0.2", 1000, 80, 300, 0, packet.TCPFlagACK, 140)
	third := testDrop(12, "10.0.0.1", "10.0.0.2", 1000, 80, 500, 0, packet.TCPFlagACK, 140)
	m.add(first)
	m.add(second)
	m.add(third)
	if got := m.match(testRetrans(20, "10.0.0.1", "10.0.0.2", 1000, 80, 100, 200)); got != nil {
		t.Fatalf("evicted match = %p, want nil", got)
	}
	if got := m.match(testRetrans(21, "10.0.0.1", "10.0.0.2", 1000, 80, 300, 400)); got != second {
		t.Fatalf("retained match = %p, want %p", got, second)
	}
}

func TestSequenceRangeOverlap(t *testing.T) {
	tests := []struct {
		name         string
		aStart, aEnd uint32
		bStart, bEnd uint32
		want         bool
	}{
		{name: "same", aStart: 10, aEnd: 20, bStart: 10, bEnd: 20, want: true},
		{name: "touching", aStart: 10, aEnd: 20, bStart: 20, bEnd: 30, want: false},
		{name: "wrap", aStart: math.MaxUint32 - 5, aEnd: 5, bStart: 0, bEnd: 10, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tcpSeqRangesOverlap(tt.aStart, tt.aEnd, tt.bStart, tt.bEnd); got != tt.want {
				t.Fatalf("tcpSeqRangesOverlap = %t, want %t", got, tt.want)
			}
		})
	}
}

func testDrop(
	ktime uint64,
	saddr, daddr string,
	sport, dport uint16,
	seq, ack uint32,
	flags uint8,
	packetLen uint32,
) *DropEvent {
	return &DropEvent{
		KtimeNS:            ktime,
		NetNamespaceCookie: 1,
		NetNamespaceInode:  2,
		PacketLen:          packetLen,
		Layers: &packet.Packet{
			Label: "IPv4/TCP",
			IPv4: &packet.IPv4{
				Version: 4, IHL: 5, Length: uint16(min(packetLen, 65535)),
				Protocol: "TCP", Saddr: net.ParseIP(saddr), Daddr: net.ParseIP(daddr),
			},
			TCP: &packet.TCP{
				Sport: sport, Dport: dport, Seq: seq, AckSeq: ack,
				DataOffset: 5, Flags: packet.TCPFlagStrings[flags], RawFlags: flags,
			},
		},
	}
}

func testRetrans(
	ktime uint64,
	saddr, daddr string,
	sport, dport uint16,
	seq, end uint32,
) *types.TCPRetransmitTracing {
	addressFamily := uint16(unix.AF_INET)
	if net.ParseIP(saddr).To4() == nil {
		addressFamily = unix.AF_INET6
	}
	return &types.TCPRetransmitTracing{
		KtimeNS: ktime, NetNamespaceCookie: 1, NetNamespaceInum: 2,
		TCPSaddr: saddr, TCPDaddr: daddr, TCPSport: sport, TCPDport: dport,
		TCPSeq: seq, TCPEndSeq: end,
		EventType: tcpRetransmitSKBEventType,
		TCPFlags:  packet.TCPFlagStrings[packet.TCPFlagACK], TCPFlagsRaw: packet.TCPFlagACK,
		AddressFamily: addressFamily,
	}
}

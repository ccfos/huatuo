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

package retransmit

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/dropwatch"
	"github.com/ccfos/huatuo/internal/packet"
)

func TestDropAndRetransmitEntriesUseSameFlowKey(t *testing.T) {
	tests := []struct {
		name               string
		sourceAddress      string
		destinationAddress string
		packet             *packet.Packet
	}{
		{
			name:               "IPv4",
			sourceAddress:      "192.0.2.1",
			destinationAddress: "198.51.100.2",
			packet: &packet.Packet{
				IPv4: &packet.IPv4{
					Saddr: net.ParseIP("192.0.2.1"),
					Daddr: net.ParseIP("198.51.100.2"),
				},
				TCP: &packet.TCP{Sport: 1000, Dport: 80},
			},
		},
		{
			name:               "IPv6",
			sourceAddress:      "2001:db8::1",
			destinationAddress: "2001:db8::2",
			packet: &packet.Packet{
				IPv6: &packet.IPv6{
					Saddr: net.ParseIP("2001:db8::1"),
					Daddr: net.ParseIP("2001:db8::2"),
				},
				TCP: &packet.TCP{Sport: 1000, Dport: 80},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dropFlow, ok := flowFromPacket(test.packet)
			if !ok {
				t.Fatal("flowFromPacket() = false")
			}

			retransmit, ok := retransmitEntryFromEvent(testRetransmitEvent(
				10,
				test.sourceAddress,
				test.destinationAddress,
				1000,
				80,
				100,
				200,
			))
			if !ok {
				t.Fatal("retransmitEntryFromEvent() = false")
			}
			if retransmit.flow != dropFlow {
				t.Fatalf(
					"retransmit flow = %+v, want drop flow %+v",
					retransmit.flow,
					dropFlow,
				)
			}
		})
	}
}

func TestRetransmitAddressesUnmapIPv4(t *testing.T) {
	event := testRetransmitEvent(10, "::ffff:192.0.2.1", "::ffff:198.51.100.2", 1000, 80, 100, 200)
	source, destination := retransmitAddresses(&event.record)
	if source != netip.MustParseAddr("192.0.2.1") || destination != netip.MustParseAddr("198.51.100.2") {
		t.Fatalf("addresses = %v, %v; want unmapped IPv4", source, destination)
	}
}

func TestFlowFromPacketRejectsMappedIPv4InIPv6Layer(t *testing.T) {
	layers := &packet.Packet{
		IPv6: &packet.IPv6{
			Saddr: net.ParseIP("::ffff:192.0.2.1"),
			Daddr: net.ParseIP("::ffff:198.51.100.2"),
		},
		TCP: &packet.TCP{Sport: 1000, Dport: 80},
	}
	if _, ok := flowFromPacket(layers); ok {
		t.Fatal("flowFromPacket() accepted mapped IPv4 addresses in IPv6 layer")
	}
}

func TestRetransmitEntryUsesABIFlags(t *testing.T) {
	event := testRetransmitEvent(
		10,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		100,
		200,
	)
	event.record.TCPFlags = packet.TCPFlagACK

	entry, ok := retransmitEntryFromEvent(event)
	if !ok {
		t.Fatal("retransmitEntryFromEvent() = false")
	}
	if entry.kind != retransmitMatchData {
		t.Fatalf("match kind = %d, want data from raw ACK flag", entry.kind)
	}
}

func TestRetransmitEntryRejectsInvalidAddressPair(t *testing.T) {
	event := testRetransmitEvent(10, "10.0.0.1", "2001:db8::2", 1000, 80, 100, 200)
	if _, ok := retransmitEntryFromEvent(event); ok {
		t.Fatal("accepted mixed mapped IPv4 and IPv6 addresses")
	}
	event.record.Family = 0
	if _, ok := retransmitEntryFromEvent(event); ok {
		t.Fatal("accepted unknown address family")
	}
}

func testDropEvent(
	t *testing.T,
	kernelObservedNS uint64,
	sourceAddress,
	destinationAddress string,
	sourcePort,
	destinationPort uint16,
	sequence,
	endSequence,
	ackSequence uint32,
	tcpFlags uint8,
) *dropEvent {
	t.Helper()
	return &dropEvent{
		kernelObservedNS: kernelObservedNS,
		metadata:         dropwatch.Metadata{Source: dropwatch.SourceSoftware},
		namespace:        namespaceID{cookie: 1, inode: 2},
		flow: flowKey{
			source: netip.AddrPortFrom(
				netip.MustParseAddr(sourceAddress),
				sourcePort,
			),
			destination: netip.AddrPortFrom(
				netip.MustParseAddr(destinationAddress),
				destinationPort,
			),
		},
		sequence:    sequence,
		endSequence: endSequence,
		ackSequence: ackSequence,
		tcpFlags:    tcpFlags,
	}
}

func TestRetransmitMatchKindUsesABI(t *testing.T) {
	tests := []struct {
		name  string
		kind  abi.TCPRetransmitEventType
		flags uint8
		want  retransmitMatchKind
	}{
		{"syn", abi.TCPRetransmitEventSKB, packet.TCPFlagSYN, retransmitMatchSYN},
		{"data", abi.TCPRetransmitEventSKB, packet.TCPFlagACK, retransmitMatchData},
		{"fin", abi.TCPRetransmitEventSKB, packet.TCPFlagACK | packet.TCPFlagFIN, retransmitMatchData},
		{"reset", abi.TCPRetransmitEventSKB, packet.TCPFlagACK | packet.TCPFlagRST, retransmitMatchUnsupported},
		{"synack hook", abi.TCPRetransmitEventSynack, 0, retransmitMatchSYNACK},
		{"synack ignores skb flags", abi.TCPRetransmitEventSynack, packet.TCPFlagRST, retransmitMatchSYNACK},
		{"probe", abi.TCPRetransmitEventTlp, packet.TCPFlagACK, retransmitMatchUnsupported},
		{"unknown", abi.TCPRetransmitEventType(255), packet.TCPFlagACK, retransmitMatchUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := abi.TCPRetransmitEvent{EventType: uint8(test.kind), TCPFlags: test.flags}
			if got := retransmitMatchKindFromRecord(&record); got != test.want {
				t.Fatalf("match kind = %v, want %v", got, test.want)
			}
		})
	}
}

func testRetransmitEvent(
	kernelObservedNS uint64,
	sourceAddress,
	destinationAddress string,
	sourcePort,
	destinationPort uint16,
	sequence,
	endSequence uint32,
) *retransmitEvent {
	source := netip.MustParseAddr(sourceAddress)
	destination := netip.MustParseAddr(destinationAddress)
	record := abi.TCPRetransmitEvent{
		KernelObservedNS:   kernelObservedNS,
		NetNamespaceCookie: 1, NetNamespaceInum: 2,
		Saddr: source.As16(), Daddr: destination.As16(),
		Family: unix.AF_INET6,
		Sport:  sourcePort, Dport: destinationPort,
		TCPSeq: sequence, TCPEndSeq: endSequence,
		EventType: uint8(abi.TCPRetransmitEventSKB),
		TCPFlags:  packet.TCPFlagACK,
	}
	if source.Is4() && destination.Is4() {
		record.Family = unix.AF_INET
		sourceBytes, destinationBytes := source.As4(), destination.As4()
		copy(record.Saddr[:], sourceBytes[:])
		copy(record.Daddr[:], destinationBytes[:])
	}
	return &retransmitEvent{record: record, observedAt: time.Unix(10, 0)}
}

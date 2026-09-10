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
	"testing"

	"github.com/ccfos/huatuo/internal/packet"
	"github.com/ccfos/huatuo/pkg/types"
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

func TestParseAddressUnmapsIPv4(t *testing.T) {
	address, ok := parseAddress("::ffff:192.0.2.1")
	if !ok {
		t.Fatal("parseAddress() = false")
	}
	want := netip.MustParseAddr("192.0.2.1")
	if address != want || !address.Is4() {
		t.Fatalf("parseAddress() = %v, want unmapped %v", address, want)
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

func TestRetransmitEntryUsesRawFlags(t *testing.T) {
	event := testRetransmitEvent(
		10,
		"10.0.0.1",
		"10.0.0.2",
		1000,
		80,
		100,
		200,
	)
	event.TCPFlags = "SYN"
	event.TCPFlagsRaw = packet.TCPFlagACK

	entry, ok := retransmitEntryFromEvent(event)
	if !ok {
		t.Fatal("retransmitEntryFromEvent() = false")
	}
	if entry.kind != retransmitMatchData {
		t.Fatalf("match kind = %d, want data from raw ACK flag", entry.kind)
	}
}

func TestRetransmitEntryRejectsInvalidAddressPair(t *testing.T) {
	tests := []struct {
		name               string
		sourceAddress      string
		destinationAddress string
	}{
		{
			name:               "invalid source address",
			sourceAddress:      "invalid",
			destinationAddress: "10.0.0.2",
		},
		{
			name:               "invalid destination address",
			sourceAddress:      "10.0.0.1",
			destinationAddress: "invalid",
		},
		{
			name:               "mixed address families",
			sourceAddress:      "10.0.0.1",
			destinationAddress: "2001:db8::2",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := testRetransmitEvent(
				10,
				test.sourceAddress,
				test.destinationAddress,
				1000,
				80,
				100,
				200,
			)
			if _, ok := retransmitEntryFromEvent(event); ok {
				t.Fatal("retransmitEntryFromEvent() accepted invalid address pair")
			}
		})
	}
}

func testDropEvent(
	t *testing.T,
	ktimeNS uint64,
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
		ktimeNS:   ktimeNS,
		namespace: namespaceID{cookie: 1, inode: 2},
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

func testRetransmitEvent(
	ktimeNS uint64,
	sourceAddress,
	destinationAddress string,
	sourcePort,
	destinationPort uint16,
	sequence,
	endSequence uint32,
) *types.TCPRetransmitTracing {
	return &types.TCPRetransmitTracing{
		KtimeNS:            ktimeNS,
		NetNamespaceCookie: 1,
		NetNamespaceInum:   2,
		TCPSaddr:           sourceAddress,
		TCPDaddr:           destinationAddress,
		TCPSport:           sourcePort,
		TCPDport:           destinationPort,
		TCPSeq:             sequence,
		TCPEndSeq:          endSequence,
		EventType:          tcpRetransmitSKBEventType,
		TCPFlags:           packet.TCPFlagStrings[packet.TCPFlagACK],
		TCPFlagsRaw:        packet.TCPFlagACK,
	}
}

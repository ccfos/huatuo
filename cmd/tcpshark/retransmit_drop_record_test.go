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
	"encoding/binary"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/packet"
)

func TestDropEventFromRecord(t *testing.T) {
	record := newIPv4DropwatchTCPRecord(40)
	record.Meta.KtimeNS = 100
	record.Meta.NetNamespaceCookie = 200
	record.Meta.NetNamespaceInum = 300
	record.StackSize = 16
	record.Stack[0], record.Stack[1] = 0x1000, 0x2000

	event, err := dropEventFromRecord(record)
	if err != nil {
		t.Fatalf("dropEventFromRecord() error = %v", err)
	}
	if event.ktimeNS != 100 ||
		event.namespace != (namespaceID{cookie: 200, inode: 300}) {
		t.Fatalf("scalar mapping = %+v", event)
	}
	if event.flow != testFlowKey(12345, 80) || event.sequence != 123 ||
		event.endSequence != 123 || event.tcpFlags != packet.TCPFlagACK {
		t.Fatalf("normalized packet = %+v", event)
	}
	if event.stackDepth != 2 || event.stackPCs[0] != 0x1000 ||
		event.stackPCs[1] != 0x2000 {
		t.Fatalf("stack = depth %d pcs %x", event.stackDepth, event.stackPCs[:2])
	}
}

func TestDropEventFromRecordUsesIPLengthForSequenceSpan(t *testing.T) {
	tests := []struct {
		name            string
		record          *abi.DropwatchPacketEvent
		wantEndSequence uint32
		wantIPv6        bool
	}{
		{name: "ipv4", record: newIPv4DropwatchTCPRecord(40), wantEndSequence: 123},
		{name: "ipv6", record: newIPv6DropwatchTCPRecord(20), wantEndSequence: 123, wantIPv6: true},
		{name: "ipv4 payload", record: newIPv4DropwatchTCPRecord(4040), wantEndSequence: 4123},
		{name: "ipv6 payload", record: newIPv6DropwatchTCPRecord(4020), wantEndSequence: 4123, wantIPv6: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.record.Meta.KtimeNS = 1
			test.record.Meta.NetNamespaceCookie = 1
			event, err := dropEventFromRecord(test.record)
			if err != nil {
				t.Fatalf("dropEventFromRecord() error = %v", err)
			}
			if event.endSequence != test.wantEndSequence {
				t.Fatalf(
					"end sequence = %d, want %d",
					event.endSequence,
					test.wantEndSequence,
				)
			}
			if event.flow.source.address.Is6() != test.wantIPv6 {
				t.Fatalf(
					"source address = %s, want IPv6 %t",
					event.flow.source.address,
					test.wantIPv6,
				)
			}
		})
	}
}

func TestDropEventFromRecordUsesRawFlags(t *testing.T) {
	const flags = packet.TCPFlagSYN | packet.TCPFlagFIN

	record := newIPv4DropwatchTCPRecord(50)
	record.Meta.KtimeNS = 1
	record.Meta.NetNamespaceCookie = 1
	record.PktHdr.Raw[33] = flags

	event, err := dropEventFromRecord(record)
	if err != nil {
		t.Fatalf("dropEventFromRecord() error = %v", err)
	}
	if event.endSequence != 135 || event.tcpFlags != flags {
		t.Fatalf(
			"normalized packet = (end %d, flags %#x), want (135, 0x3)",
			event.endSequence,
			event.tcpFlags,
		)
	}
}

func TestDropEventFromRecordParseErrorKeepsScalars(t *testing.T) {
	record := &abi.DropwatchPacketEvent{}
	record.Meta.KtimeNS = 100
	record.Meta.NetNamespaceCookie = 200
	record.PktHdr.RawLen = 1

	event, err := dropEventFromRecord(record)
	if err == nil {
		t.Fatal("dropEventFromRecord() error = nil, want parse error")
	}
	if event == nil || event.ktimeNS != 100 ||
		event.namespace != (namespaceID{cookie: 200}) {
		t.Fatalf("scalar event = %+v", event)
	}
	if event.flow.source.address.IsValid() || event.flow.destination.address.IsValid() {
		t.Fatalf("flow = %+v, want invalid zero value", event.flow)
	}
}

func TestDropEventFromRecordRejectsNil(t *testing.T) {
	event, err := dropEventFromRecord(nil)
	if err == nil || event != nil {
		t.Fatalf("dropEventFromRecord(nil) = (%+v, %v), want nil and error", event, err)
	}
}

func TestDropEventFromRecordRejectsZeroKtime(t *testing.T) {
	record := newIPv4DropwatchTCPRecord(40)

	event, err := dropEventFromRecord(record)
	if err == nil || event != nil {
		t.Fatalf("dropEventFromRecord() = (%+v, %v), want nil and error", event, err)
	}
}

func newIPv4DropwatchTCPRecord(
	ipTotalLength uint16,
) *abi.DropwatchPacketEvent {
	record := &abi.DropwatchPacketEvent{}
	record.PktHdr.EthProto = 0x0800
	record.PktHdr.RawLen = 40
	record.PktHdr.PacketLenBytes = uint32(ipTotalLength)
	record.PktHdr.Raw[0] = 0x45
	binary.BigEndian.PutUint16(record.PktHdr.Raw[2:], ipTotalLength)
	record.PktHdr.Raw[8] = 64
	record.PktHdr.Raw[9] = 6
	record.PktHdr.Raw[12], record.PktHdr.Raw[15] = 10, 1
	record.PktHdr.Raw[16], record.PktHdr.Raw[19] = 10, 2
	binary.BigEndian.PutUint16(record.PktHdr.Raw[20:], 12345)
	binary.BigEndian.PutUint16(record.PktHdr.Raw[22:], 80)
	binary.BigEndian.PutUint32(record.PktHdr.Raw[24:], 123)
	record.PktHdr.Raw[32] = 0x50
	record.PktHdr.Raw[33] = packet.TCPFlagACK
	return record
}

func newIPv6DropwatchTCPRecord(
	ipPayloadLength uint16,
) *abi.DropwatchPacketEvent {
	record := &abi.DropwatchPacketEvent{}
	record.PktHdr.EthProto = 0x86dd
	record.PktHdr.RawLen = 60
	record.PktHdr.PacketLenBytes = uint32(ipPayloadLength) + 40
	record.PktHdr.Raw[0] = 0x60
	binary.BigEndian.PutUint16(record.PktHdr.Raw[4:], ipPayloadLength)
	record.PktHdr.Raw[6] = 6
	record.PktHdr.Raw[7] = 64
	record.PktHdr.Raw[8], record.PktHdr.Raw[23] = 0x20, 1
	record.PktHdr.Raw[24], record.PktHdr.Raw[39] = 0x20, 2
	binary.BigEndian.PutUint16(record.PktHdr.Raw[40:], 12345)
	binary.BigEndian.PutUint16(record.PktHdr.Raw[42:], 80)
	binary.BigEndian.PutUint32(record.PktHdr.Raw[44:], 123)
	record.PktHdr.Raw[52] = 0x50
	record.PktHdr.Raw[53] = packet.TCPFlagACK
	return record
}

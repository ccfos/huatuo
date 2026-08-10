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
	"encoding/binary"
	"testing"

	"huatuo-bamai/internal/bpf/abi"
)

func TestDropEventFromRecord(t *testing.T) {
	record := newIPv4DropwatchTCPRecord(40, 40)
	record.Meta.KtimeNS = 100
	record.Meta.NetNSCookie = 200
	record.Meta.NetNSInum = 300
	record.StackSize = 16
	record.Stack[0], record.Stack[1] = 0x1000, 0x2000

	event, err := DropEventFromRecord(record)
	if err != nil {
		t.Fatalf("DropEventFromRecord: %v", err)
	}
	if event.KtimeNS != 100 || event.NetNamespaceCookie != 200 ||
		event.NetNamespaceInode != 300 || event.PacketLen != 40 {
		t.Fatalf("unexpected scalar mapping: %+v", event)
	}
	if event.Layers == nil || event.Layers.TCP == nil || event.Layers.TCP.Seq != 123 {
		t.Fatalf("unexpected parsed packet: %+v", event.Layers)
	}
	if event.StackDepth != 2 || event.StackPCs[0] != 0x1000 || event.StackPCs[1] != 0x2000 {
		t.Fatalf("unexpected stack mapping: depth=%d pcs=%x", event.StackDepth, event.StackPCs[:2])
	}
}

func TestDropEventFromRecordPreservesCanonicalL3PacketLength(t *testing.T) {
	tests := []struct {
		name      string
		record    *abi.DropwatchPacketEvent
		packetLen uint32
		ipv6      bool
	}{
		{
			name:      "ipv4",
			record:    newIPv4DropwatchTCPRecord(40, 40),
			packetLen: 40,
		},
		{
			name:      "ipv6",
			record:    newIPv6DropwatchTCPRecord(20, 60),
			packetLen: 60,
			ipv6:      true,
		},
		{
			name:      "ipv4_gso",
			record:    newIPv4DropwatchTCPRecord(40, 4040),
			packetLen: 4040,
		},
		{
			name:      "ipv6_gso",
			record:    newIPv6DropwatchTCPRecord(20, 4060),
			packetLen: 4060,
			ipv6:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.record.Meta.KtimeNS = 1
			tt.record.Meta.NetNSCookie = 1
			event, err := DropEventFromRecord(tt.record)
			if err != nil {
				t.Fatalf("DropEventFromRecord: %v", err)
			}
			if event.PacketLen != tt.packetLen {
				t.Fatalf("PacketLen = %d, want %d", event.PacketLen, tt.packetLen)
			}
			if event.Layers == nil || event.Layers.TCP == nil {
				t.Fatalf("Layers = %+v, want TCP", event.Layers)
			}
			if tt.ipv6 && event.Layers.IPv6 == nil {
				t.Fatalf("Layers = %+v, want IPv6", event.Layers)
			}
			if !tt.ipv6 && event.Layers.IPv4 == nil {
				t.Fatalf("Layers = %+v, want IPv4", event.Layers)
			}
		})
	}
}

func TestDropEventFromRecordParseErrorKeepsScalars(t *testing.T) {
	record := &abi.DropwatchPacketEvent{}
	record.Meta.KtimeNS = 100
	record.Meta.NetNSCookie = 200
	record.PktHdr.RawLen = 1

	event, err := DropEventFromRecord(record)
	if err == nil {
		t.Fatal("DropEventFromRecord error = nil, want packet parse error")
	}
	if event == nil || event.KtimeNS != 100 || event.NetNamespaceCookie != 200 {
		t.Fatalf("scalar event = %+v", event)
	}
	if event.Layers != nil {
		t.Fatalf("Layers = %+v, want nil", event.Layers)
	}
}

func TestLoadDropwatchBPFReadError(t *testing.T) {
	if _, err := LoadDropwatchBPF("/definitely/not/a/dropwatch/object", "tcp", 0, 0); err == nil {
		t.Fatal("LoadDropwatchBPF error = nil")
	}
}

func newIPv4DropwatchTCPRecord(
	ipTotalLen uint16,
	packetLen uint32,
) *abi.DropwatchPacketEvent {
	record := &abi.DropwatchPacketEvent{}
	record.PktHdr.EthProto = 0x0800
	record.PktHdr.RawLen = 40
	record.PktHdr.PktLen = packetLen
	record.PktHdr.Raw[0] = 0x45
	binary.BigEndian.PutUint16(record.PktHdr.Raw[2:], ipTotalLen)
	record.PktHdr.Raw[8] = 64
	record.PktHdr.Raw[9] = 6
	record.PktHdr.Raw[12], record.PktHdr.Raw[15] = 10, 1
	record.PktHdr.Raw[16], record.PktHdr.Raw[19] = 10, 2
	binary.BigEndian.PutUint16(record.PktHdr.Raw[20:], 12345)
	binary.BigEndian.PutUint16(record.PktHdr.Raw[22:], 80)
	binary.BigEndian.PutUint32(record.PktHdr.Raw[24:], 123)
	record.PktHdr.Raw[32] = 0x50
	record.PktHdr.Raw[33] = 0x10
	return record
}

func newIPv6DropwatchTCPRecord(
	ipPayloadLen uint16,
	packetLen uint32,
) *abi.DropwatchPacketEvent {
	record := &abi.DropwatchPacketEvent{}
	record.PktHdr.EthProto = 0x86dd
	record.PktHdr.RawLen = 60
	record.PktHdr.PktLen = packetLen
	record.PktHdr.Raw[0] = 0x60
	binary.BigEndian.PutUint16(record.PktHdr.Raw[4:], ipPayloadLen)
	record.PktHdr.Raw[6] = 6
	record.PktHdr.Raw[7] = 64
	record.PktHdr.Raw[8], record.PktHdr.Raw[23] = 0x20, 1
	record.PktHdr.Raw[24], record.PktHdr.Raw[39] = 0x20, 2
	binary.BigEndian.PutUint16(record.PktHdr.Raw[40:], 12345)
	binary.BigEndian.PutUint16(record.PktHdr.Raw[42:], 80)
	binary.BigEndian.PutUint32(record.PktHdr.Raw[44:], 123)
	record.PktHdr.Raw[52] = 0x50
	record.PktHdr.Raw[53] = 0x10
	return record
}

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

package dropwatch

import (
	"encoding/binary"
	"testing"

	"github.com/ccfos/huatuo/internal/bpf/abi"
)

func tcpRecord() *abi.DropwatchPacketEvent {
	record := &abi.DropwatchPacketEvent{}
	record.PktHdr.EthProto = 0x0800
	record.PktHdr.RawLen = 40
	record.PktHdr.Raw[0] = 0x45
	binary.BigEndian.PutUint16(record.PktHdr.Raw[2:], 40)
	record.PktHdr.Raw[9] = 6
	record.PktHdr.Raw[12], record.PktHdr.Raw[15] = 10, 1
	record.PktHdr.Raw[16], record.PktHdr.Raw[19] = 10, 2
	binary.BigEndian.PutUint16(record.PktHdr.Raw[20:], 12345)
	binary.BigEndian.PutUint16(record.PktHdr.Raw[22:], 80)
	record.PktHdr.Raw[32] = 0x50
	return record
}

func TestDecodePacketBoundsAndOwnership(t *testing.T) {
	for _, size := range []uint16{40, 256, 65535} {
		record := tcpRecord()
		record.PktHdr.RawLen = size
		decoded, err := DecodePacket(record)
		if err != nil || decoded == nil || decoded.TCP == nil || decoded.TCP.Sport != 12345 {
			t.Fatalf("length %d: %v, %v", size, decoded, err)
		}
		clear(record.PktHdr.Raw[:])
		if decoded.IPv4.Saddr.String() != "10.0.0.1" || decoded.TCP.Sport != 12345 {
			t.Fatal("packet aliases record")
		}
	}
}

func TestDecodePacketPartialAndInvalid(t *testing.T) {
	record := tcpRecord()
	record.PktHdr.RawLen = 20
	decoded, err := DecodePacket(record)
	if err != nil || decoded == nil || decoded.IPv4 == nil || decoded.TCP != nil {
		t.Fatalf("partial packet: %v, %v", decoded, err)
	}
	if decoded, err := DecodePacket(nil); err == nil || decoded != nil {
		t.Fatal("nil record accepted")
	}
	record.PktHdr.RawLen = 0
	if decoded, err := DecodePacket(record); err == nil || decoded != nil {
		t.Fatalf("empty packet: %v, %v", decoded, err)
	}
}

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
	"errors"

	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/packet"
)

// DecodePacket returns the captured protocol layers. It preserves packet.Parse's
// partial-frame behavior: decoded layers may be returned without an error even
// when later layers are truncated. The packet does not borrow record's memory.
func DecodePacket(record *abi.DropwatchPacketEvent) (*packet.Packet, error) {
	if record == nil {
		return nil, errors.New("decode dropwatch packet: nil record")
	}
	rawLength := min(record.PktHdr.RawLen, uint16(packet.RawCapacity))
	header := packet.Hdr{
		EthProto:  record.PktHdr.EthProto,
		RawLen:    uint8(rawLength),
		HasEthHdr: uint8(record.PktHdr.HasEthHdr),
		SkState:   uint8(record.PktHdr.SkState),
		Raw:       record.PktHdr.Raw,
	}
	return packet.Parse(&header)
}

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

// Package netcorrelate joins dropwatch input to TCP retransmissions.
package netcorrelate

import (
	"fmt"
	"os"
	"time"

	"huatuo-bamai/internal/bpf"
	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/packet"
	"huatuo-bamai/internal/pcapfilter"
	"huatuo-bamai/internal/symbol"
)

const hardwareDropSection = "raw_tracepoint/devlink_trap_report"

// DropEvent contains only the retained fields needed for matching and local
// stack output. PacketLen is the normalized L3 length needed for GSO ranges.
type DropEvent struct {
	KtimeNS            uint64
	NetNamespaceCookie uint64
	NetNamespaceInode  uint32
	PacketLen          uint32
	Layers             *packet.Packet
	StackDepth         uint8
	StackPCs           [symbol.KsymStackMaxDepth]uint64
}

// LoadDropwatchBPF loads an unattached dropwatch object with its capture
// filter, device mode, and rate limit fixed for the lifetime of the object.
func LoadDropwatchBPF(
	bpfPath string,
	filterExpr string,
	deviceMode uint32,
	maxEventsPerSecond uint64,
) (bpf.BPF, error) {
	bpfBytes, err := os.ReadFile(bpfPath)
	if err != nil {
		return nil, fmt.Errorf("read dropwatch BPF object %q: %w", bpfPath, err)
	}

	constants := bpf.NewRateLimiter("dropwatch", maxEventsPerSecond).Constants(
		map[string]any{"filter_dev_mode": deviceMode},
	)

	name := fmt.Sprintf("dropwatch_%d.o", time.Now().UnixNano())
	loaded, err := pcapfilter.Load(
		name,
		bpfBytes,
		filterExpr,
		constants,
		hardwareDropSection,
	)
	if err != nil {
		return nil, fmt.Errorf("load dropwatch BPF object %q: %w", bpfPath, err)
	}
	return loaded, nil
}

// DropEventFromRecord copies a reusable perf record into retained correlation
// storage. A packet parse error is returned with the usable scalar event.
func DropEventFromRecord(record *abi.DropwatchPacketEvent) (*DropEvent, error) {
	if record == nil {
		return nil, fmt.Errorf("convert dropwatch perf record: nil record")
	}

	rawLen := record.PktHdr.RawLen
	if rawLen > packet.RawCapacity {
		rawLen = packet.RawCapacity
	}
	hdr := packet.Hdr{
		EthProto:  record.PktHdr.EthProto,
		RawLen:    uint8(rawLen),
		HasEthHdr: uint8(record.PktHdr.HasEthHdr),
		SkState:   uint8(record.PktHdr.SkState),
		Raw:       record.PktHdr.Raw,
	}
	layers, parseErr := packet.Parse(&hdr)

	event := &DropEvent{
		KtimeNS:            record.Meta.KtimeNS,
		NetNamespaceCookie: record.Meta.NetNSCookie,
		NetNamespaceInode:  record.Meta.NetNSInum,
		PacketLen:          record.PktHdr.PktLen,
		Layers:             layers,
	}
	if record.StackSize > 0 && record.StackSize <= uint64(len(record.Stack))*8 {
		depth := record.StackSize / 8
		event.StackDepth = uint8(depth)
		copy(event.StackPCs[:depth], record.Stack[:depth])
	}
	if parseErr != nil {
		return event, fmt.Errorf("parse dropwatch packet: %w", parseErr)
	}
	return event, nil
}

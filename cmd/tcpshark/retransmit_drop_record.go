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
	"fmt"
	"net"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/packet"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
	"github.com/ccfos/huatuo/internal/utils/kernaddr"
	"github.com/ccfos/huatuo/pkg/types"
)

var retransmitEventTypeNames = map[abi.TCPRetransmitEventType]string{
	abi.TCPRetransmitEventSKB:    tcpRetransmitSKBEventType,
	abi.TCPRetransmitEventSynack: tcpRetransmitSYNACKEventType,
	abi.TCPRetransmitEventTlp:    "tcp_send_loss_probe",
}

func retransmitEventFromRecord(
	record *abi.TCPRetransmitEvent,
	sourceType string,
) *types.TCPRetransmitTracing {
	rawEventType := abi.TCPRetransmitEventType(record.EventType)
	tcpFlagsRaw := record.TCPFlags
	if rawEventType == abi.TCPRetransmitEventSynack {
		tcpFlagsRaw = packet.TCPFlagSYN | packet.TCPFlagACK
	}

	classification := classifyRetransmit(record)
	eventType, ok := retransmitEventTypeNames[rawEventType]
	if !ok {
		eventType = "unknown"
	}

	var sourceAddress, destinationAddress string
	switch record.Family {
	case unix.AF_INET:
		sourceAddress = net.IP(record.Saddr[:net.IPv4len]).String()
		destinationAddress = net.IP(record.Daddr[:net.IPv4len]).String()
	case unix.AF_INET6:
		sourceAddress = net.IP(record.Saddr[:]).String()
		destinationAddress = net.IP(record.Daddr[:]).String()
	}

	return &types.TCPRetransmitTracing{
		ObservedTimestamp:   time.Now().UTC().Format(time.RFC3339Nano),
		KtimeNS:             record.KtimeNS,
		TCPReason:           classification.reason.String(),
		Source:              sourceType,
		Comm:                bytesutil.ToStr(record.Comm[:]),
		PID:                 record.TGIDPID >> 32,
		MemoryCgroupCSSAddr: kernaddr.Format(record.MemcgCSSAddr),
		NetNamespaceCookie:  record.NetNamespaceCookie,
		NetNamespaceInum:    record.NetNamespaceInum,
		TCPState:            packet.TCPStateName(uint8(record.State)),
		TCPSaddr:            sourceAddress,
		TCPDaddr:            destinationAddress,
		TCPSport:            record.Sport,
		TCPDport:            record.Dport,
		TCPSeq:              record.TCPSeq,
		TCPAckSeq:           record.TCPAck,
		TCPEndSeq:           record.TCPEndSeq,
		TCPFlags:            packet.TCPFlagStrings[tcpFlagsRaw],
		TCPFlagsRaw:         tcpFlagsRaw,
		Phase:               classification.phase.String(),
		EventType:           eventType,
		CaState:             record.CaState,
		IcskRetransmits:     record.IcskRetransmits,
		IcskPending:         record.IcskPending,
		ReordSeen:           record.ReordSeen,
		DsackDups:           record.DsackDups,
		SkbAddr:             kernaddr.Format(record.SKBAddr),
	}
}

// dropEventFromRecord leaves flow invalid when packet evidence cannot be
// normalized. The correlator can then record the delivery without carrying a
// separate validity flag or overstating coverage.
func dropEventFromRecord(record *abi.DropwatchPacketEvent) (*dropEvent, error) {
	if record == nil {
		return nil, fmt.Errorf("convert dropwatch perf record: nil record")
	}
	if record.Meta.KtimeNS == 0 {
		return nil, fmt.Errorf("convert dropwatch perf record: zero ktime")
	}

	event := &dropEvent{
		ktimeNS: record.Meta.KtimeNS,
		namespace: namespaceID{
			cookie: record.Meta.NetNamespaceCookie,
			inode:  record.Meta.NetNamespaceInum,
		},
	}
	if record.StackSize > 0 && record.StackSize <= uint64(len(record.Stack))*8 {
		depth := record.StackSize / 8
		event.stackDepth = uint8(depth)
		copy(event.stackPCs[:depth], record.Stack[:depth])
	}

	rawLength := record.PktHdr.RawLen
	if rawLength > packet.RawCapacity {
		rawLength = packet.RawCapacity
	}
	header := packet.Hdr{
		EthProto:  record.PktHdr.EthProto,
		RawLen:    uint8(rawLength),
		HasEthHdr: uint8(record.PktHdr.HasEthHdr),
		SkState:   uint8(record.PktHdr.SkState),
		Raw:       record.PktHdr.Raw,
	}
	layers, parseErr := packet.Parse(&header)
	if parseErr != nil {
		return event, fmt.Errorf("parse dropwatch packet: %w", parseErr)
	}

	flow, ok := flowFromPacket(layers)
	if !ok {
		return event, nil
	}
	// TODO: Handle GSO once dropwatch exposes a reliable L3 length.
	span, ok := packet.TCPSequenceSpan(layers)
	if !ok {
		return event, nil
	}

	event.flow = flow
	event.sequence = layers.TCP.Seq
	event.endSequence = layers.TCP.Seq + span
	event.ackSequence = layers.TCP.AckSeq
	event.tcpFlags = layers.TCP.RawFlags
	return event, nil
}

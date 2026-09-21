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
	"fmt"
	"net/netip"
	"time"

	"golang.org/x/sys/unix"

	"github.com/ccfos/huatuo/internal/bpf/abi"
	"github.com/ccfos/huatuo/internal/dropwatch"
	"github.com/ccfos/huatuo/internal/packet"
	"github.com/ccfos/huatuo/internal/timeutil"
	"github.com/ccfos/huatuo/internal/utils/bytesutil"
	"github.com/ccfos/huatuo/internal/utils/kernaddr"
	"github.com/ccfos/huatuo/pkg/types"
)

// retransmitEvent holds the capture data until output; it does not retain a
// formatted tracing record. Channel delivery transfers an independent copy.
type retransmitEvent struct {
	record     abi.TCPRetransmitEvent
	observedAt time.Time
}

var retransmitEventTypeNames = map[abi.TCPRetransmitEventType]string{
	abi.TCPRetransmitEventSKB:    "tcp_retransmit_skb",
	abi.TCPRetransmitEventSynack: "tcp_retransmit_synack",
	abi.TCPRetransmitEventTlp:    "tcp_send_loss_probe",
}

func (e *retransmitEvent) tracing(sourceType string) (*types.TCPRetransmitTracing, error) {
	record := &e.record
	kernelObservedTimestamp, err := timeutil.KtimeToTimestamp(record.KernelObservedNS)
	if err != nil {
		return nil, fmt.Errorf("convert TCP retransmit kernel observation time: %w", err)
	}
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
	source, destination := retransmitAddresses(record)
	if source.IsValid() {
		sourceAddress = source.String()
		destinationAddress = destination.String()
	}

	return &types.TCPRetransmitTracing{
		ObservedTimestamp:       timeutil.Timestamp{Time: e.observedAt},
		KernelObservedTimestamp: &kernelObservedTimestamp,
		KernelObservedNS:        record.KernelObservedNS,
		TCPReason:               classification.reason.String(),
		Source:                  sourceType,
		Comm:                    bytesutil.ToStr(record.Comm[:]),
		PID:                     record.TGIDPID >> 32,
		MemoryCgroupCSSAddr:     kernaddr.Format(record.MemcgCSSAddr),
		NetNamespaceCookie:      record.NetNamespaceCookie,
		NetNamespaceInum:        record.NetNamespaceInum,
		TCPState:                packet.TCPStateName(uint8(record.State)),
		TCPSaddr:                sourceAddress,
		TCPDaddr:                destinationAddress,
		TCPSport:                record.Sport,
		TCPDport:                record.Dport,
		TCPSeq:                  record.TCPSeq,
		TCPAckSeq:               record.TCPAck,
		TCPEndSeq:               record.TCPEndSeq,
		TCPFlags:                packet.TCPFlagStrings[tcpFlagsRaw],
		TCPFlagsRaw:             tcpFlagsRaw,
		Phase:                   classification.phase.String(),
		EventType:               eventType,
		CaState:                 record.CaState,
		IcskRetransmits:         record.IcskRetransmits,
		IcskPending:             record.IcskPending,
		ReordSeen:               record.ReordSeen,
		DsackDups:               record.DsackDups,
		SkbAddr:                 kernaddr.Format(record.SKBAddr),
	}, nil
}

func retransmitAddresses(record *abi.TCPRetransmitEvent) (netip.Addr, netip.Addr) {
	switch record.Family {
	case unix.AF_INET:
		return netip.AddrFrom4([4]byte(record.Saddr[:4])), netip.AddrFrom4([4]byte(record.Daddr[:4]))
	case unix.AF_INET6:
		return netip.AddrFrom16(record.Saddr).Unmap(), netip.AddrFrom16(record.Daddr).Unmap()
	default:
		return netip.Addr{}, netip.Addr{}
	}
}

// dropEventFromRecord leaves flow invalid when packet evidence cannot be
// normalized. The correlator can then record the delivery without carrying a
// separate validity flag or overstating coverage.
func dropEventFromRecord(record *abi.DropwatchPacketEvent) (*dropEvent, error) {
	if record == nil {
		return nil, fmt.Errorf("convert dropwatch perf record: nil record")
	}
	if record.Meta.KernelObservedNS == 0 {
		return nil, fmt.Errorf("convert dropwatch perf record: zero kernel observation timestamp")
	}

	event := &dropEvent{
		kernelObservedNS: record.Meta.KernelObservedNS,
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

	layers, parseErr := dropwatch.DecodePacket(record)
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

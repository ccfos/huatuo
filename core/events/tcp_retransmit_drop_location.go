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

package events

import (
	"fmt"

	"huatuo-bamai/pkg/types"
)

type TCPRetransmitDropCausal uint8

const (
	TCPRetransmitDropNone TCPRetransmitDropCausal = iota
	TCPRetransmitDropDirect
	TCPRetransmitDrop4Tuple
	TCPRetransmitNoDrop
)

func (c TCPRetransmitDropCausal) String() string {
	switch c {
	case TCPRetransmitDropDirect:
		return "drop_direct"
	case TCPRetransmitDrop4Tuple:
		return "drop_4tuple"
	case TCPRetransmitNoDrop:
		return "no_drop"
	default:
		return ""
	}
}

type connKey string

func makeConnKey(saddr, daddr string, sport, dport uint16) connKey {
	if saddr < daddr || (saddr == daddr && sport <= dport) {
		return connKey(fmt.Sprintf("%s:%d-%s:%d", saddr, sport, daddr, dport))
	}
	return connKey(fmt.Sprintf("%s:%d-%s:%d", daddr, dport, saddr, sport))
}

func CorrelateDropTCPRetransmit(
	drop *types.DropWatchTracing,
	retransmit *types.TCPRetransmitTracing,
) TCPRetransmitDropCausal {
	return ClassifyDropwatchTCPRetransmitCausal(drop, retransmit)
}

func ClassifyDropwatchTCPRetransmitCausal(
	drop *types.DropWatchTracing,
	retransmit *types.TCPRetransmitTracing,
) TCPRetransmitDropCausal {
	isSameSKB := drop.PacketSkbAddr != "" &&
		retransmit.SkbAddr != "" &&
		drop.PacketSkbAddr == retransmit.SkbAddr
	if isSameSKB {
		return TCPRetransmitDropDirect
	}

	if drop.Layers == nil || drop.Layers.TCP == nil {
		return TCPRetransmitDropNone
	}

	return dropLayerMatchTCPRetransmitCausal(drop, retransmit)
}

func dropLayerMatchTCPRetransmitCausal(
	drop *types.DropWatchTracing,
	retransmit *types.TCPRetransmitTracing,
) TCPRetransmitDropCausal {
	var saddr, daddr string
	switch {
	case drop.Layers.IPv4 != nil:
		saddr = drop.Layers.IPv4.Saddr.String()
		daddr = drop.Layers.IPv4.Daddr.String()
	case drop.Layers.IPv6 != nil:
		saddr = drop.Layers.IPv6.Saddr.String()
		daddr = drop.Layers.IPv6.Daddr.String()
	default:
		return TCPRetransmitDropNone
	}
	sport := drop.Layers.TCP.Sport
	dport := drop.Layers.TCP.Dport

	if (saddr == retransmit.TCPSaddr && daddr == retransmit.TCPDaddr &&
		sport == retransmit.TCPSport && dport == retransmit.TCPDport) ||
		(saddr == retransmit.TCPDaddr && daddr == retransmit.TCPSaddr &&
			sport == retransmit.TCPDport && dport == retransmit.TCPSport) {
		return TCPRetransmitDrop4Tuple
	}
	return TCPRetransmitDropNone
}

func makeTCPRetransmitKey(ev *types.TCPRetransmitTracing) connKey {
	return makeConnKey(ev.TCPSaddr, ev.TCPDaddr, ev.TCPSport, ev.TCPDport)
}

func BuildTCPRetransmitCorrelationReport(
	drop *types.DropWatchTracing,
	retransmit *types.TCPRetransmitTracing,
) string {
	causal := ClassifyDropwatchTCPRetransmitCausal(drop, retransmit)

	switch causal {
	case TCPRetransmitDropDirect:
		return "DROP caused RETRANS directly (same sk_buff): " +
			retransmit.TCPSaddr + ":" + fmtU16(retransmit.TCPSport) + " > " +
			retransmit.TCPDaddr + ":" + fmtU16(retransmit.TCPDport) +
			" phase=" + retransmit.Phase + " tcp_reason=" + retransmit.TCPReason

	case TCPRetransmitDrop4Tuple:
		return "DROP and RETRANS share same connection: " +
			retransmit.TCPSaddr + ":" + fmtU16(retransmit.TCPSport) + " > " +
			retransmit.TCPDaddr + ":" + fmtU16(retransmit.TCPDport) +
			" phase=" + retransmit.Phase + " tcp_reason=" + retransmit.TCPReason

	default:
		return ""
	}
}

func fmtU16(v uint16) string {
	return fmt.Sprintf("%d", v)
}

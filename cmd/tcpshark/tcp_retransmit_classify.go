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
	"golang.org/x/sys/unix"

	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/pkg/types"
)

const (
	// Keep these values synchronized with RETRANSMIT_EVENT_* in
	// bpf/tcp_retransmit.c. C preprocessor macros are not part of BTF, so the
	// ABI generator cannot generate these constants.
	tcpRetransmitEventSKB    = 1
	tcpRetransmitEventSYNACK = 2
	tcpRetransmitEventTLP    = 3
)

const (
	tcpCAOpen uint8 = iota
	tcpCADisorder
	tcpCACWR
	tcpCARecovery
	tcpCALoss
)

func classifyEvent(ev *abi.TCPRetransmitEvent, tcpFlags string) (types.TCPRetransmitPhase, types.TCPRetransmitReason) {
	switch ev.EventType {
	case tcpRetransmitEventSYNACK:
		return types.TCPRetransmitPhaseConnect, types.TCPRetransmitReasonRTO
	case tcpRetransmitEventTLP:
		return types.TCPRetransmitPhaseData, types.TCPRetransmitReasonTLP
	default:
		return classifyRetransmit(
			uint8(ev.State),
			tcpFlags,
			ev.CaState,
			ev.ReordSeen,
			ev.DsackDups,
		)
	}
}

func classifyRetransmit(
	skStateNum uint8,
	tcpFlags string,
	caState uint8,
	reordSeen uint32,
	dsackDups uint32,
) (types.TCPRetransmitPhase, types.TCPRetransmitReason) {
	phase := phaseFromState(skStateNum, tcpFlags)
	reason := reasonFromTree(caState, reordSeen, dsackDups, phase)
	return phase, reason
}

func phaseFromState(skStateNum uint8, tcpFlags string) types.TCPRetransmitPhase {
	switch skStateNum {
	case unix.BPF_TCP_SYN_SENT:
		return types.TCPRetransmitPhaseConnect
	case unix.BPF_TCP_SYN_RECV, unix.BPF_TCP_NEW_SYN_RECV:
		return types.TCPRetransmitPhaseConnect
	case unix.BPF_TCP_ESTABLISHED:
		return types.TCPRetransmitPhaseData
	case unix.BPF_TCP_FIN_WAIT1, unix.BPF_TCP_CLOSE_WAIT,
		unix.BPF_TCP_LAST_ACK, unix.BPF_TCP_CLOSING:
		return types.TCPRetransmitPhaseClose
	case unix.BPF_TCP_FIN_WAIT2, unix.BPF_TCP_TIME_WAIT:
		return types.TCPRetransmitPhaseClose
	default:
		return phaseFromFlags(tcpFlags)
	}
}

func reasonFromTree(
	caState uint8,
	reordSeen uint32,
	dsackDups uint32,
	phase types.TCPRetransmitPhase,
) types.TCPRetransmitReason {
	switch caState {
	case tcpCALoss:
		return types.TCPRetransmitReasonRTO
	case tcpCARecovery:
		if isReorderProne(reordSeen, dsackDups) {
			return types.TCPRetransmitReasonReorderProneFast
		}
		return types.TCPRetransmitReasonFast
	default:
		return reasonFromPhase(phase)
	}
}

func isReorderProne(reordSeen, dsackDups uint32) bool {
	return reordSeen > 0 || dsackDups > 0
}

func phaseFromFlags(flags string) types.TCPRetransmitPhase {
	if flags == "" {
		return types.TCPRetransmitPhaseData
	}
	if containsFlag(flags, "SYN") && !containsFlag(flags, "ACK") {
		return types.TCPRetransmitPhaseConnect
	}
	if containsFlag(flags, "SYN") && containsFlag(flags, "ACK") {
		return types.TCPRetransmitPhaseConnect
	}
	if containsFlag(flags, "FIN") {
		return types.TCPRetransmitPhaseClose
	}
	return types.TCPRetransmitPhaseData
}

func reasonFromPhase(phase types.TCPRetransmitPhase) types.TCPRetransmitReason {
	if phase == types.TCPRetransmitPhaseConnect {
		return types.TCPRetransmitReasonRTO
	}
	if phase == types.TCPRetransmitPhaseClose {
		return types.TCPRetransmitReasonRTO
	}
	return types.TCPRetransmitReasonUnknown
}

func containsFlag(flags, flag string) bool {
	for i := 0; i <= len(flags)-len(flag); i++ {
		if flags[i:i+len(flag)] == flag {
			return true
		}
	}
	return false
}

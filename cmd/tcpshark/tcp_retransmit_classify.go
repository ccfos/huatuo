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
	"huatuo-bamai/internal/packet"
	"huatuo-bamai/pkg/types"
)

type tcpRetransmitClassification struct {
	valid  bool
	phase  types.TCPRetransmitPhase
	reason types.TCPRetransmitReason
}

var retransmitEventClassifications = [...]tcpRetransmitClassification{
	abi.TCPRetransmitEventSynack: {
		valid:  true,
		phase:  types.TCPRetransmitPhaseConnect,
		reason: types.TCPRetransmitReasonRTO,
	},
	abi.TCPRetransmitEventTlp: {
		valid:  true,
		phase:  types.TCPRetransmitPhaseData,
		reason: types.TCPRetransmitReasonTLP,
	},
}

func classifyRetransmit(ev *abi.TCPRetransmitEvent) tcpRetransmitClassification {
	eventType := abi.TCPRetransmitEventType(ev.EventType)
	if eventType < abi.TCPRetransmitEventTlp+1 {
		classification := retransmitEventClassifications[eventType]
		if classification.valid {
			return classification
		}
	}

	phase := classifySKBPhase(uint8(ev.State), ev.TCPFlags)
	return tcpRetransmitClassification{
		phase:  phase,
		reason: classifySKBReason(ev, phase),
	}
}

func classifySKBPhase(state, tcpFlags uint8) types.TCPRetransmitPhase {
	switch state {
	case unix.BPF_TCP_SYN_SENT, unix.BPF_TCP_SYN_RECV, unix.BPF_TCP_NEW_SYN_RECV:
		return types.TCPRetransmitPhaseConnect
	case unix.BPF_TCP_ESTABLISHED:
		return types.TCPRetransmitPhaseData
	case unix.BPF_TCP_FIN_WAIT1, unix.BPF_TCP_CLOSE_WAIT,
		unix.BPF_TCP_LAST_ACK, unix.BPF_TCP_CLOSING,
		unix.BPF_TCP_FIN_WAIT2, unix.BPF_TCP_TIME_WAIT:
		return types.TCPRetransmitPhaseClose
	default:
		return phaseFromFlags(tcpFlags)
	}
}

func classifySKBReason(
	ev *abi.TCPRetransmitEvent,
	phase types.TCPRetransmitPhase,
) types.TCPRetransmitReason {
	switch abi.TCPRetransmitCaState(ev.CaState) {
	case abi.TCPRetransmitCaLoss:
		return types.TCPRetransmitReasonRTO
	case abi.TCPRetransmitCaRecovery:
		if ev.ReordSeen != 0 || ev.DsackDups != 0 {
			return types.TCPRetransmitReasonReorderProneFast
		}
		return types.TCPRetransmitReasonFast
	default:
		if phase == types.TCPRetransmitPhaseConnect ||
			phase == types.TCPRetransmitPhaseClose {
			return types.TCPRetransmitReasonRTO
		}
		return types.TCPRetransmitReasonUnknown
	}
}

func phaseFromFlags(flags uint8) types.TCPRetransmitPhase {
	if flags&packet.TCPFlagSYN != 0 {
		return types.TCPRetransmitPhaseConnect
	}
	if flags&packet.TCPFlagFIN != 0 {
		return types.TCPRetransmitPhaseClose
	}
	return types.TCPRetransmitPhaseData
}

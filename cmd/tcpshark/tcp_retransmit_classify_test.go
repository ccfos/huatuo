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
	"testing"

	"golang.org/x/sys/unix"

	"huatuo-bamai/internal/bpf/abi"
	"huatuo-bamai/internal/packet"
	"huatuo-bamai/pkg/types"
)

func TestRetransmitClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		eventType  abi.TCPRetransmitEventType
		skState    uint8
		tcpFlags   uint8
		caState    abi.TCPRetransmitCaState
		reordSeen  uint32
		dsackDups  uint32
		wantPhase  types.TCPRetransmitPhase
		wantReason types.TCPRetransmitReason
	}{
		{
			name:       "SYN retrans (SYN_SENT)",
			skState:    unix.BPF_TCP_SYN_SENT,
			tcpFlags:   packet.TCPFlagSYN,
			caState:    abi.TCPRetransmitCaLoss,
			wantPhase:  types.TCPRetransmitPhaseConnect,
			wantReason: types.TCPRetransmitReasonRTO,
		},
		{
			name:       "SYN retrans (SYN_SENT) no caState",
			skState:    unix.BPF_TCP_SYN_SENT,
			tcpFlags:   packet.TCPFlagSYN,
			caState:    abi.TCPRetransmitCaOpen,
			wantPhase:  types.TCPRetransmitPhaseConnect,
			wantReason: types.TCPRetransmitReasonRTO,
		},
		{
			name:       "SYNACK retrans (SYN_RECV)",
			skState:    unix.BPF_TCP_SYN_RECV,
			tcpFlags:   packet.TCPFlagSYN | packet.TCPFlagACK,
			caState:    abi.TCPRetransmitCaLoss,
			wantPhase:  types.TCPRetransmitPhaseConnect,
			wantReason: types.TCPRetransmitReasonRTO,
		},
		{
			name:       "SYNACK retrans (NEW_SYN_RECV)",
			skState:    unix.BPF_TCP_NEW_SYN_RECV,
			tcpFlags:   packet.TCPFlagSYN | packet.TCPFlagACK,
			caState:    abi.TCPRetransmitCaOpen,
			wantPhase:  types.TCPRetransmitPhaseConnect,
			wantReason: types.TCPRetransmitReasonRTO,
		},

		{
			name:       "RTO retrans (ESTABLISHED + Loss)",
			skState:    unix.BPF_TCP_ESTABLISHED,
			tcpFlags:   packet.TCPFlagACK,
			caState:    abi.TCPRetransmitCaLoss,
			wantPhase:  types.TCPRetransmitPhaseData,
			wantReason: types.TCPRetransmitReasonRTO,
		},

		{
			name:       "Fast retrans (Recovery)",
			skState:    unix.BPF_TCP_ESTABLISHED,
			tcpFlags:   packet.TCPFlagACK,
			caState:    abi.TCPRetransmitCaRecovery,
			wantPhase:  types.TCPRetransmitPhaseData,
			wantReason: types.TCPRetransmitReasonFast,
		},

		{
			name:       "Unknown (Open)",
			skState:    unix.BPF_TCP_ESTABLISHED,
			tcpFlags:   packet.TCPFlagACK,
			caState:    abi.TCPRetransmitCaOpen,
			wantPhase:  types.TCPRetransmitPhaseData,
			wantReason: types.TCPRetransmitReasonUnknown,
		},
		{
			name:       "Unknown (Disorder)",
			skState:    unix.BPF_TCP_ESTABLISHED,
			tcpFlags:   packet.TCPFlagACK,
			caState:    abi.TCPRetransmitCaDisorder,
			wantPhase:  types.TCPRetransmitPhaseData,
			wantReason: types.TCPRetransmitReasonUnknown,
		},

		{
			name:       "Fast retrans + reord_seen>0 -> reorder_prone_fast",
			skState:    unix.BPF_TCP_ESTABLISHED,
			tcpFlags:   packet.TCPFlagACK,
			caState:    abi.TCPRetransmitCaRecovery,
			reordSeen:  1,
			wantPhase:  types.TCPRetransmitPhaseData,
			wantReason: types.TCPRetransmitReasonReorderProneFast,
		},
		{
			name:       "Fast retrans + dsack_dups>0 -> reorder_prone_fast",
			skState:    unix.BPF_TCP_ESTABLISHED,
			tcpFlags:   packet.TCPFlagACK,
			caState:    abi.TCPRetransmitCaRecovery,
			dsackDups:  2,
			wantPhase:  types.TCPRetransmitPhaseData,
			wantReason: types.TCPRetransmitReasonReorderProneFast,
		},
		{
			name:       "RTO + reorder fields (irrelevant for RTO)",
			skState:    unix.BPF_TCP_ESTABLISHED,
			tcpFlags:   packet.TCPFlagACK,
			caState:    abi.TCPRetransmitCaLoss,
			reordSeen:  5,
			dsackDups:  3,
			wantPhase:  types.TCPRetransmitPhaseData,
			wantReason: types.TCPRetransmitReasonRTO,
		},

		{
			name:       "FIN retrans (FIN_WAIT1)",
			skState:    unix.BPF_TCP_FIN_WAIT1,
			tcpFlags:   packet.TCPFlagFIN | packet.TCPFlagACK,
			caState:    abi.TCPRetransmitCaLoss,
			wantPhase:  types.TCPRetransmitPhaseClose,
			wantReason: types.TCPRetransmitReasonRTO,
		},
		{
			name:       "FIN retrans (LAST_ACK) no caState",
			skState:    unix.BPF_TCP_LAST_ACK,
			tcpFlags:   packet.TCPFlagFIN | packet.TCPFlagACK,
			caState:    abi.TCPRetransmitCaOpen,
			wantPhase:  types.TCPRetransmitPhaseClose,
			wantReason: types.TCPRetransmitReasonRTO,
		},
		{
			name:       "CLOSE_WAIT residual data Recovery",
			skState:    unix.BPF_TCP_CLOSE_WAIT,
			tcpFlags:   packet.TCPFlagACK,
			caState:    abi.TCPRetransmitCaRecovery,
			wantPhase:  types.TCPRetransmitPhaseClose,
			wantReason: types.TCPRetransmitReasonFast,
		},
		{
			name:       "CLOSE_WAIT no caState -> RTO fallback",
			skState:    unix.BPF_TCP_CLOSE_WAIT,
			tcpFlags:   packet.TCPFlagACK,
			caState:    abi.TCPRetransmitCaOpen,
			wantPhase:  types.TCPRetransmitPhaseClose,
			wantReason: types.TCPRetransmitReasonRTO,
		},
		{
			name:       "SYNACK event overrides socket state",
			eventType:  abi.TCPRetransmitEventSynack,
			skState:    unix.BPF_TCP_ESTABLISHED,
			tcpFlags:   packet.TCPFlagACK,
			caState:    abi.TCPRetransmitCaRecovery,
			wantPhase:  types.TCPRetransmitPhaseConnect,
			wantReason: types.TCPRetransmitReasonRTO,
		},
		{
			name:       "TLP event overrides socket state",
			eventType:  abi.TCPRetransmitEventTlp,
			skState:    unix.BPF_TCP_SYN_SENT,
			tcpFlags:   packet.TCPFlagSYN,
			caState:    abi.TCPRetransmitCaLoss,
			wantPhase:  types.TCPRetransmitPhaseData,
			wantReason: types.TCPRetransmitReasonTLP,
		},
		{
			name:       "unknown state falls back to SYN flag",
			skState:    unix.BPF_TCP_CLOSE,
			tcpFlags:   packet.TCPFlagSYN,
			caState:    abi.TCPRetransmitCaOpen,
			wantPhase:  types.TCPRetransmitPhaseConnect,
			wantReason: types.TCPRetransmitReasonRTO,
		},
		{
			name:       "unknown state falls back to FIN flag",
			skState:    unix.BPF_TCP_LISTEN,
			tcpFlags:   packet.TCPFlagFIN,
			caState:    abi.TCPRetransmitCaOpen,
			wantPhase:  types.TCPRetransmitPhaseClose,
			wantReason: types.TCPRetransmitReasonRTO,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := classifyRetransmit(&abi.TCPRetransmitEvent{
				EventType: uint8(tt.eventType),
				State:     uint32(tt.skState),
				TCPFlags:  tt.tcpFlags,
				CaState:   uint8(tt.caState),
				ReordSeen: tt.reordSeen,
				DsackDups: tt.dsackDups,
			})
			if got.phase != tt.wantPhase {
				t.Errorf("phase: got %v, want %v", got.phase, tt.wantPhase)
			}
			if got.reason != tt.wantReason {
				t.Errorf("reason: got %v, want %v", got.reason, tt.wantReason)
			}
		})
	}
}

func TestRetransmitClassificationAllStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state uint8
		want  types.TCPRetransmitPhase
	}{
		{name: "established", state: unix.BPF_TCP_ESTABLISHED, want: types.TCPRetransmitPhaseData},
		{name: "syn sent", state: unix.BPF_TCP_SYN_SENT, want: types.TCPRetransmitPhaseConnect},
		{name: "syn recv", state: unix.BPF_TCP_SYN_RECV, want: types.TCPRetransmitPhaseConnect},
		{name: "fin wait 1", state: unix.BPF_TCP_FIN_WAIT1, want: types.TCPRetransmitPhaseClose},
		{name: "fin wait 2", state: unix.BPF_TCP_FIN_WAIT2, want: types.TCPRetransmitPhaseClose},
		{name: "time wait", state: unix.BPF_TCP_TIME_WAIT, want: types.TCPRetransmitPhaseClose},
		{name: "close fallback", state: unix.BPF_TCP_CLOSE, want: types.TCPRetransmitPhaseData},
		{name: "close wait", state: unix.BPF_TCP_CLOSE_WAIT, want: types.TCPRetransmitPhaseClose},
		{name: "last ack", state: unix.BPF_TCP_LAST_ACK, want: types.TCPRetransmitPhaseClose},
		{name: "listen fallback", state: unix.BPF_TCP_LISTEN, want: types.TCPRetransmitPhaseData},
		{name: "closing", state: unix.BPF_TCP_CLOSING, want: types.TCPRetransmitPhaseClose},
		{name: "new syn recv", state: unix.BPF_TCP_NEW_SYN_RECV, want: types.TCPRetransmitPhaseConnect},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			classification := classifyRetransmit(&abi.TCPRetransmitEvent{
				State:    uint32(tt.state),
				TCPFlags: packet.TCPFlagACK,
				CaState:  uint8(abi.TCPRetransmitCaOpen),
			})
			if classification.phase != tt.want {
				t.Errorf("phase = %v, want %v", classification.phase, tt.want)
			}
		})
	}
}

func BenchmarkClassifyRetransmit(b *testing.B) {
	benchmarks := []struct {
		name  string
		event abi.TCPRetransmitEvent
	}{
		{
			name: "state",
			event: abi.TCPRetransmitEvent{
				State:    unix.BPF_TCP_ESTABLISHED,
				TCPFlags: packet.TCPFlagACK,
				CaState:  uint8(abi.TCPRetransmitCaRecovery),
			},
		},
		{
			name: "flags",
			event: abi.TCPRetransmitEvent{
				State:    unix.BPF_TCP_CLOSE,
				TCPFlags: packet.TCPFlagFIN | packet.TCPFlagACK,
				CaState:  uint8(abi.TCPRetransmitCaOpen),
			},
		},
	}

	for i := range benchmarks {
		benchmark := &benchmarks[i]
		b.Run(benchmark.name, func(b *testing.B) {
			b.ReportAllocs()

			var classification tcpRetransmitClassification
			for b.Loop() {
				classification = classifyRetransmit(&benchmark.event)
			}
			_ = classification
		})
	}
}

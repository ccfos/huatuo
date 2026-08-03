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
	"testing"

	"golang.org/x/sys/unix"

	"huatuo-bamai/pkg/types"
)

func TestRetransmitClassification(t *testing.T) {
	tests := []struct {
		name       string
		skState    uint8
		tcpFlags   string
		caState    uint8
		reordSeen  uint32
		dsackDups  uint32
		wantPhase  types.TCPRetransmitPhase
		wantReason types.TCPRetransmitReason
	}{
		// === Connect phase ===
		{
			name:       "SYN retrans (SYN_SENT)",
			skState:    unix.BPF_TCP_SYN_SENT,
			tcpFlags:   "SYN",
			caState:    tcpCALoss,
			wantPhase:  types.TCPRetransmitPhaseConnect,
			wantReason: types.TCPRetransmitReasonRTO,
		},
		{
			name:       "SYN retrans (SYN_SENT) no caState",
			skState:    unix.BPF_TCP_SYN_SENT,
			tcpFlags:   "SYN",
			caState:    tcpCAOpen,
			wantPhase:  types.TCPRetransmitPhaseConnect,
			wantReason: types.TCPRetransmitReasonRTO,
		},
		{
			name:       "SYNACK retrans (SYN_RECV)",
			skState:    unix.BPF_TCP_SYN_RECV,
			tcpFlags:   "SYN|ACK",
			caState:    tcpCALoss,
			wantPhase:  types.TCPRetransmitPhaseConnect,
			wantReason: types.TCPRetransmitReasonRTO,
		},
		{
			name:       "SYNACK retrans (NEW_SYN_RECV)",
			skState:    unix.BPF_TCP_NEW_SYN_RECV,
			tcpFlags:   "SYN|ACK",
			caState:    tcpCAOpen,
			wantPhase:  types.TCPRetransmitPhaseConnect,
			wantReason: types.TCPRetransmitReasonRTO,
		},

		// === Data phase: RTO ===
		{
			name:       "RTO retrans (ESTABLISHED + Loss)",
			skState:    unix.BPF_TCP_ESTABLISHED,
			tcpFlags:   "ACK",
			caState:    tcpCALoss,
			wantPhase:  types.TCPRetransmitPhaseData,
			wantReason: types.TCPRetransmitReasonRTO,
		},

		// === Data phase: Fast retransmit ===
		{
			name:       "Fast retrans (Recovery)",
			skState:    unix.BPF_TCP_ESTABLISHED,
			tcpFlags:   "ACK",
			caState:    tcpCARecovery,
			wantPhase:  types.TCPRetransmitPhaseData,
			wantReason: types.TCPRetransmitReasonFast,
		},

		// === Data phase: Unknown ===
		{
			name:       "Unknown (Open)",
			skState:    unix.BPF_TCP_ESTABLISHED,
			tcpFlags:   "ACK",
			caState:    tcpCAOpen,
			wantPhase:  types.TCPRetransmitPhaseData,
			wantReason: types.TCPRetransmitReasonUnknown,
		},
		{
			name:       "Unknown (Disorder)",
			skState:    unix.BPF_TCP_ESTABLISHED,
			tcpFlags:   "ACK",
			caState:    tcpCADisorder,
			wantPhase:  types.TCPRetransmitPhaseData,
			wantReason: types.TCPRetransmitReasonUnknown,
		},

		// === Reorder-prone fast retransmit ===
		{
			name:       "Fast retrans + reord_seen>0 -> reorder_prone_fast",
			skState:    unix.BPF_TCP_ESTABLISHED,
			tcpFlags:   "ACK",
			caState:    tcpCARecovery,
			reordSeen:  1,
			wantPhase:  types.TCPRetransmitPhaseData,
			wantReason: types.TCPRetransmitReasonReorderProneFast,
		},
		{
			name:       "Fast retrans + dsack_dups>0 -> reorder_prone_fast",
			skState:    unix.BPF_TCP_ESTABLISHED,
			tcpFlags:   "ACK",
			caState:    tcpCARecovery,
			dsackDups:  2,
			wantPhase:  types.TCPRetransmitPhaseData,
			wantReason: types.TCPRetransmitReasonReorderProneFast,
		},
		{
			name:       "RTO + reorder fields (irrelevant for RTO)",
			skState:    unix.BPF_TCP_ESTABLISHED,
			tcpFlags:   "ACK",
			caState:    tcpCALoss,
			reordSeen:  5,
			dsackDups:  3,
			wantPhase:  types.TCPRetransmitPhaseData,
			wantReason: types.TCPRetransmitReasonRTO,
		},

		// === Close phase ===
		{
			name:       "FIN retrans (FIN_WAIT1)",
			skState:    unix.BPF_TCP_FIN_WAIT1,
			tcpFlags:   "FIN|ACK",
			caState:    tcpCALoss,
			wantPhase:  types.TCPRetransmitPhaseClose,
			wantReason: types.TCPRetransmitReasonRTO,
		},
		{
			name:       "FIN retrans (LAST_ACK) no caState",
			skState:    unix.BPF_TCP_LAST_ACK,
			tcpFlags:   "FIN|ACK",
			caState:    tcpCAOpen,
			wantPhase:  types.TCPRetransmitPhaseClose,
			wantReason: types.TCPRetransmitReasonRTO,
		},
		{
			name:       "CLOSE_WAIT residual data Recovery",
			skState:    unix.BPF_TCP_CLOSE_WAIT,
			tcpFlags:   "ACK|PSH",
			caState:    tcpCARecovery,
			wantPhase:  types.TCPRetransmitPhaseClose,
			wantReason: types.TCPRetransmitReasonFast,
		},
		{
			name:       "CLOSE_WAIT no caState -> RTO fallback",
			skState:    unix.BPF_TCP_CLOSE_WAIT,
			tcpFlags:   "ACK",
			caState:    tcpCAOpen,
			wantPhase:  types.TCPRetransmitPhaseClose,
			wantReason: types.TCPRetransmitReasonRTO,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotPhase, gotReason := classifyRetransmit(
				tt.skState,
				tt.tcpFlags,
				tt.caState,
				tt.reordSeen,
				tt.dsackDups,
			)
			if gotPhase != tt.wantPhase {
				t.Errorf("phase: got %v, want %v", gotPhase, tt.wantPhase)
			}
			if gotReason != tt.wantReason {
				t.Errorf("reason: got %v, want %v", gotReason, tt.wantReason)
			}
		})
	}
}

func TestPhaseAndReasonStrings(t *testing.T) {
	phases := []types.TCPRetransmitPhase{
		types.TCPRetransmitPhaseConnect,
		types.TCPRetransmitPhaseData,
		types.TCPRetransmitPhaseClose,
		types.TCPRetransmitPhase(99),
	}
	wantPhaseStrs := []string{"connect", "data", "close", "unknown"}
	for i, p := range phases {
		if s := p.String(); s != wantPhaseStrs[i] {
			t.Errorf("phase %d: got %q, want %q", p, s, wantPhaseStrs[i])
		}
	}

	reasons := []types.TCPRetransmitReason{
		types.TCPRetransmitReasonRTO,
		types.TCPRetransmitReasonFast,
		types.TCPRetransmitReasonReorderProneFast,
		types.TCPRetransmitReasonTLP,
		types.TCPRetransmitReasonSpurious,
		types.TCPRetransmitReasonUnknown,
		types.TCPRetransmitReason(99),
	}
	wantReasonStrs := []string{
		"RTO",
		"fast_retransmit",
		"reorder_prone_fast",
		"TLP",
		"spurious",
		"unknown",
		"unknown",
	}
	for i, r := range reasons {
		if s := r.String(); s != wantReasonStrs[i] {
			t.Errorf("reason %d: got %q, want %q", r, s, wantReasonStrs[i])
		}
	}
}

func TestReorderProneClassification(t *testing.T) {
	tests := []struct {
		reordSeen uint32
		dsackDups uint32
		want      bool
	}{
		{0, 0, false},
		{1, 0, true},
		{0, 1, true},
		{3, 2, true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("reord_seen=%d,dsack_dups=%d", tt.reordSeen, tt.dsackDups), func(t *testing.T) {
			if got := isReorderProne(tt.reordSeen, tt.dsackDups); got != tt.want {
				t.Errorf("isReorderProne(%d, %d) = %v, want %v", tt.reordSeen, tt.dsackDups, got, tt.want)
			}
		})
	}
}

func TestRetransmitClassificationAllStates(t *testing.T) {
	stateNames := []string{
		"", "ESTABLISHED", "SYN_SENT", "SYN_RECV",
		"FIN_WAIT1", "FIN_WAIT2", "TIME_WAIT", "CLOSE",
		"CLOSE_WAIT", "LAST_ACK", "LISTEN", "CLOSING", "NEW_SYN_RECV",
	}
	connectStates := map[uint8]bool{
		unix.BPF_TCP_SYN_SENT:     true,
		unix.BPF_TCP_SYN_RECV:     true,
		unix.BPF_TCP_NEW_SYN_RECV: true,
	}
	dataStates := map[uint8]bool{unix.BPF_TCP_ESTABLISHED: true}
	closeStates := map[uint8]bool{
		unix.BPF_TCP_FIN_WAIT1:  true,
		unix.BPF_TCP_FIN_WAIT2:  true,
		unix.BPF_TCP_TIME_WAIT:  true,
		unix.BPF_TCP_CLOSE_WAIT: true,
		unix.BPF_TCP_LAST_ACK:   true,
		unix.BPF_TCP_CLOSING:    true,
	}

	for state := uint8(unix.BPF_TCP_ESTABLISHED); state <= unix.BPF_TCP_NEW_SYN_RECV; state++ {
		state := state
		t.Run(fmt.Sprintf("state=%s(%d)", stateNames[state], state), func(t *testing.T) {
			phase, _ := classifyRetransmit(state, "ACK", tcpCAOpen, 0, 0)

			if connectStates[state] && phase != types.TCPRetransmitPhaseConnect {
				t.Errorf("state %s(%d): expected Connect phase, got %v", stateNames[state], state, phase)
			}
			if dataStates[state] && phase != types.TCPRetransmitPhaseData {
				t.Errorf("state %s(%d): expected Data phase, got %v", stateNames[state], state, phase)
			}
			if closeStates[state] && phase != types.TCPRetransmitPhaseClose {
				t.Errorf("state %s(%d): expected Close phase, got %v", stateNames[state], state, phase)
			}
		})
	}
}

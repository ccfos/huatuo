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

package types

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestTCPRetransmitTracingRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		ev   *TCPRetransmitTracing
	}{
		{
			name: "full event",
			ev: &TCPRetransmitTracing{
				ObservedTimestamp:   "2026-07-08T09:19:52.042035335Z",
				KtimeNS:             123456789,
				TCPReason:           "reorder_prone_fast",
				Source:              "events",
				Comm:                "kube-apiserver",
				PID:                 1234,
				ContainerID:         "abc123",
				MemoryCgroupCSSAddr: "0xffff888012345678",
				NetNamespaceCookie:  0x2000,
				NetNamespaceInum:    4026531992,
				TCPSaddr:            "10.0.0.1",
				TCPDaddr:            "10.0.0.2",
				TCPSport:            6443,
				TCPDport:            58244,
				TCPState:            "ESTABLISHED",
				Phase:               "data",
				EventType:           "tcp_retransmit_skb",
				CaState:             3,
				IcskRetransmits:     0,
				IcskPending:         6,
				ReordSeen:           10,
				DsackDups:           2,
				TCPSeq:              123456,
				TCPAckSeq:           789012,
				TCPEndSeq:           123999,
				TCPFlags:            "ACK|FIN",
				SkbAddr:             "0xffff888012345678",
				DropLocation:        "unknown",
				CorrelationReasons: []CorrelationReason{
					CorrelationReasonStartupHistoryIncomplete,
					CorrelationReasonPerfEventsLost,
				},
				DropwatchPerfStatus: &DropwatchPerfStatus{
					PerfLost:    1,
					RateLimited: 2,
				},
				DropStack: "kfree_skb_reason+0x1",
			},
		},
		{
			name: "minimal event",
			ev: &TCPRetransmitTracing{
				ObservedTimestamp: "2026-07-08T00:00:00Z",
				TCPReason:         "RTO",
				TCPSaddr:          "::",
				TCPDaddr:          "::",
				TCPSport:          80,
				TCPDport:          443,
				TCPState:          "ESTABLISHED",
				Phase:             "data",
				EventType:         "tcp_retransmit_skb",
			},
		},
		{
			name: "synack zero sequence",
			ev: &TCPRetransmitTracing{
				ObservedTimestamp: "2026-07-08T00:00:00Z",
				TCPReason:         "RTO",
				TCPSaddr:          "10.0.0.1",
				TCPDaddr:          "10.0.0.2",
				TCPSport:          6443,
				TCPDport:          50000,
				TCPState:          "NEW_SYN_RECV",
				Phase:             "connect",
				EventType:         "tcp_retransmit_synack",
			},
		},
		{
			name: "tlp event",
			ev: &TCPRetransmitTracing{
				ObservedTimestamp: "2026-07-08T00:00:00Z",
				TCPReason:         "TLP",
				TCPSaddr:          "10.0.0.1",
				TCPDaddr:          "10.0.0.2",
				TCPSport:          6443,
				TCPDport:          50000,
				TCPState:          "ESTABLISHED",
				Phase:             "data",
				EventType:         "tcp_send_loss_probe",
				TCPSeq:            123,
				TCPAckSeq:         100,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.ev)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			var got TCPRetransmitTracing
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}

			if diff := cmp.Diff(tt.ev, &got); diff != "" {
				t.Errorf("round-trip mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTCPRetransmitTracingOmitEmpty(t *testing.T) {
	ev := &TCPRetransmitTracing{
		ObservedTimestamp: "2026-07-08T00:00:00Z",
		TCPReason:         "RTO",
		TCPSaddr:          "10.0.0.1",
		TCPDaddr:          "10.0.0.2",
		TCPSport:          80,
		TCPDport:          443,
		TCPState:          "ESTABLISHED",
		Phase:             "data",
		EventType:         "tcp_retransmit_skb",
	}

	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal to map: %v", err)
	}

	if got := raw["tcp_reason"]; got != "RTO" {
		t.Errorf("tcp_reason = %v, want RTO", got)
	}
	if _, ok := raw["reason"]; ok {
		t.Error("reason field should be absent")
	}
	if _, ok := raw["family"]; ok {
		t.Error("family field should be absent")
	}
	wantFields := map[string]any{
		"tcp_state":   "ESTABLISHED",
		"tcp_saddr":   "10.0.0.1",
		"tcp_daddr":   "10.0.0.2",
		"tcp_sport":   float64(80),
		"tcp_dport":   float64(443),
		"tcp_seq":     float64(0),
		"tcp_ack_seq": float64(0),
	}
	for field, want := range wantFields {
		if got := raw[field]; got != want {
			t.Errorf("%s = %v, want %v", field, got, want)
		}
	}
	for _, field := range []string{"saddr", "daddr", "sport", "dport", "tcp_ack"} {
		if _, ok := raw[field]; ok {
			t.Errorf("legacy field %q should be absent", field)
		}
	}

	omitFields := []string{
		"ktime_ns",
		"container_id", "memory_cgroup_css_addr", "net_namespace_cookie", "net_namespace_inum",
		"reord_seen", "dsack_dups", "tcp_end_seq", "tcp_flags",
		"skb_addr", "drop_location", "correlation_reasons",
		"dropwatch_perf_status", "drop_stack", "source",
	}
	for _, f := range omitFields {
		if _, ok := raw[f]; ok {
			t.Errorf("omitempty field %q should be absent, got %v", f, raw[f])
		}
	}
}

func TestTCPRetransmitPhaseString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		phase TCPRetransmitPhase
		want  string
	}{
		{name: "connect", phase: TCPRetransmitPhaseConnect, want: "connect"},
		{name: "data", phase: TCPRetransmitPhaseData, want: "data"},
		{name: "close", phase: TCPRetransmitPhaseClose, want: "close"},
		{name: "unknown", phase: TCPRetransmitPhase(99), want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.phase.String(); got != tt.want {
				t.Errorf("TCPRetransmitPhase(%d).String() = %q, want %q", tt.phase, got, tt.want)
			}
		})
	}
}

func TestTCPRetransmitReasonString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reason TCPRetransmitReason
		want   string
	}{
		{name: "rto", reason: TCPRetransmitReasonRTO, want: "RTO"},
		{name: "fast", reason: TCPRetransmitReasonFast, want: "fast_retransmit"},
		{name: "reorder prone", reason: TCPRetransmitReasonReorderProneFast, want: "reorder_prone_fast"},
		{name: "tlp", reason: TCPRetransmitReasonTLP, want: "TLP"},
		{name: "spurious", reason: TCPRetransmitReasonSpurious, want: "spurious"},
		{name: "unknown", reason: TCPRetransmitReasonUnknown, want: "unknown"},
		{name: "out of range", reason: TCPRetransmitReason(99), want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.reason.String(); got != tt.want {
				t.Errorf("TCPRetransmitReason(%d).String() = %q, want %q", tt.reason, got, tt.want)
			}
		})
	}
}

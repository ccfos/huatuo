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

// TCPRetransmitPhase is the connection state-machine stage for a TCP retransmission.
type TCPRetransmitPhase uint8

const (
	TCPRetransmitPhaseConnect TCPRetransmitPhase = iota
	TCPRetransmitPhaseData
	TCPRetransmitPhaseClose
)

func (p TCPRetransmitPhase) String() string {
	switch p {
	case TCPRetransmitPhaseConnect:
		return "connect"
	case TCPRetransmitPhaseData:
		return "data"
	case TCPRetransmitPhaseClose:
		return "close"
	default:
		return "unknown"
	}
}

// TCPRetransmitReason is the trigger subcategory within a phase.
type TCPRetransmitReason uint8

const (
	TCPRetransmitReasonRTO TCPRetransmitReason = iota
	TCPRetransmitReasonFast
	TCPRetransmitReasonReorderProneFast
	TCPRetransmitReasonTLP
	TCPRetransmitReasonSpurious
	TCPRetransmitReasonUnknown
)

func (r TCPRetransmitReason) String() string {
	switch r {
	case TCPRetransmitReasonRTO:
		return "RTO"
	case TCPRetransmitReasonFast:
		return "fast_retransmit"
	case TCPRetransmitReasonReorderProneFast:
		return "reorder_prone_fast"
	case TCPRetransmitReasonTLP:
		return "TLP"
	case TCPRetransmitReasonSpurious:
		return "spurious"
	default:
		return "unknown"
	}
}

// TCPRetransmitTracing is the canonical JSON schema for a TCP retransmission event.
type TCPRetransmitTracing struct {
	ObservedTimestamp   string `json:"observed_timestamp"`
	TCPReason           string `json:"tcp_reason"` // "RTO", "fast_retransmit", "reorder_prone_fast", "TLP", "spurious", "unknown"
	Source              string `json:"source,omitempty"`
	Comm                string `json:"comm"`
	PID                 uint64 `json:"pid"`
	ContainerID         string `json:"container_id,omitempty"`
	MemoryCgroupCSSAddr string `json:"memory_cgroup_css_addr,omitempty"`
	NetNamespaceCookie  uint64 `json:"net_namespace_cookie,omitempty"`
	NetNamespaceInum    uint32 `json:"net_namespace_inum,omitempty"`

	// TCP connection and packet fields. For tcp_retransmit_skb,
	// tcp_seq/tcp_end_seq come from TCP_SKB_CB(skb), and tcp_ack_seq comes
	// from tcp_sk(sk)->rcv_nxt. The skb in tcp_retransmit_skb is headerless,
	// so tcphdr.seq/ack_seq are not reliable. tcp_flags is the rendered TCP
	// flag set, e.g. "ACK|PSH". For tcp_retransmit_synack, tcp_flags is
	// derived from the event type. For tcp_send_loss_probe, tcp_seq/tcp_ack_seq
	// contain snd_nxt/snd_una and the remaining TCP metadata is unavailable.
	TCPState  string `json:"tcp_state"` // e.g. "ESTABLISHED", "SYN_SENT", "SYN_RECV"
	TCPSaddr  string `json:"tcp_saddr"`
	TCPDaddr  string `json:"tcp_daddr"`
	TCPSport  uint16 `json:"tcp_sport"`
	TCPDport  uint16 `json:"tcp_dport"`
	TCPSeq    uint32 `json:"tcp_seq"`
	TCPAckSeq uint32 `json:"tcp_ack_seq"`
	TCPEndSeq uint32 `json:"tcp_end_seq,omitempty"`
	TCPFlags  string `json:"tcp_flags,omitempty"`

	// Phase classification.
	Phase string `json:"phase"` // "connect", "data", "close"

	// Event discriminator.
	EventType string `json:"event_type"` // "tcp_retransmit_skb", "tcp_retransmit_synack", or "tcp_send_loss_probe"

	// Congestion control state (raw BPF fields).
	CaState         uint8 `json:"ca_state"`         // icsk_ca_state: 0=Open, 3=Recovery, 4=Loss
	IcskRetransmits uint8 `json:"icsk_retransmits"` // current retrans counter for the connection
	// IcskPending is the raw inet_connection_sock timer state: 0=None,
	// 1=RTO, 3=Probe0, 5=TLP, and 6=REO. Modern kernels keep 2 (DACK)
	// in icsk_ack.pending; the meaning of 4 depends on the kernel version.
	IcskPending uint8 `json:"icsk_pending"`

	ReordSeen uint32 `json:"reord_seen,omitempty"` // tp->reord_seen (cumulative)
	DsackDups uint32 `json:"dsack_dups,omitempty"` // tp->dsack_dups (cumulative)

	// Kernel internals.
	SkbAddr string `json:"skb_addr,omitempty"` // the sk_buff pointer being retransmitted

	// Correlation with dropwatch / netdev_hw.
	DropLocation string `json:"drop_location,omitempty"` // "host_software" or "network_or_host_hardware"
}

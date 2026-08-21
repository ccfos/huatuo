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

#ifndef __BPF_ABI_TCP_RETRANSMIT_H__
#define __BPF_ABI_TCP_RETRANSMIT_H__

#include "bpf_abi.h"

enum tcp_retransmit_event_type {
	TCP_RETRANSMIT_EVENT_UNKNOWN = 0,
	TCP_RETRANSMIT_EVENT_SKB,
	TCP_RETRANSMIT_EVENT_SYNACK,
	TCP_RETRANSMIT_EVENT_TLP,
};

enum tcp_retransmit_ca_state {
	TCP_RETRANSMIT_CA_OPEN = 0,
	TCP_RETRANSMIT_CA_DISORDER,
	TCP_RETRANSMIT_CA_CWR,
	TCP_RETRANSMIT_CA_RECOVERY,
	TCP_RETRANSMIT_CA_LOSS,
};

struct tcp_retransmit_event {
	u64 ktime_ns;
	u64 tgid_pid;
	u64 memcg_css_addr;
	u64 skb_addr;
	u64 netns_cookie;
	u32 netns_inum;
	u32 state;
	u32 reord_seen;
	u32 dsack_dups;
	u32 tcp_seq;
	u32 tcp_ack;
	u32 tcp_end_seq;
	u16 sport;
	u16 dport;
	u16 family;
	u8  ca_state;
	u8  icsk_retransmits;
	u8  event_type;
	/* ICSK_TIME_*: 0=None, 1=RTO, 3=Probe0, 5=TLP, 6=REO.
	 * Modern kernels keep 2 (DACK) in icsk_ack.pending; 4 is
	 * kernel-version dependent. */
	u8  icsk_pending;
	u8  tcp_flags;
	u8  saddr[16];
	u8  daddr[16];
	u8  comm[COMPAT_TASK_COMM_LEN];
	u8  _pad[1];
};

BPF_ABI_EXPORT(tcp_retransmit_event);
BPF_ABI_EXPORT_ENUM(tcp_retransmit_event_type);
BPF_ABI_EXPORT_ENUM(tcp_retransmit_ca_state);

#endif /* __BPF_ABI_TCP_RETRANSMIT_H__ */

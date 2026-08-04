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

#ifndef __BPF_ABI_NET_RX_LATENCY_H__
#define __BPF_ABI_NET_RX_LATENCY_H__

#include "bpf_abi.h"

/*
 * Addresses are stored as 16-byte fields so a single layout serves both
 * families: IPv4 occupies the low 4 bytes (network order, rest zero), IPv6
 * uses all 16. addr_family (AF_INET / AF_INET6) tells userspace how to
 * format them.
 */
struct net_rx_latency_event {
	u8 comm[COMPAT_TASK_COMM_LEN];
	u64 latency;
	u64 tgid_pid;
	u64 pkt_len;
	u16 tcp_sport;
	u16 tcp_dport;
	u8 addr_family;
	u8 pad1[3];
	u8 tcp_saddr[16];
	u8 tcp_daddr[16];
	u32 tcp_seq;
	u32 tcp_ack_seq;
	u8 tcp_state;
	u8 lat_stage;
	u8 pad[2];
	u8 netdev_name[IFNAMSIZ];
	u32 netns_inum;
	u64 net_cookie;
};

BPF_ABI_EXPORT(net_rx_latency_event);

#endif /* __BPF_ABI_NET_RX_LATENCY_H__ */

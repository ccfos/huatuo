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

#ifndef __BPF_ABI_DROPWATCH_H__
#define __BPF_ABI_DROPWATCH_H__

#include "bpf_abi.h"

#define DROPWATCH_PACKET_RAW_LEN 120

struct dropwatch_packet_meta {
	u64 ktime_ns;
	u64 tgid_pid;
	u64 net_cookie;
	u64 kfree_skb_addr;
	u64 memcg_css_addr;
	u32 ifindex;
	u32 dev_flags;
	u32 queue_mapping;
	u32 drop_reason;
	u32 net_inum;
	u8 dev_name[IFNAMSIZ];
	u8 comm[COMPAT_TASK_COMM_LEN];
};

struct dropwatch_packet_raw {
	u16 eth_proto;
	u16 raw_len;
	u16 has_eth_hdr;
	u16 pad;
	u32 pkt_len;
	u32 sk_state;
	u8 raw[DROPWATCH_PACKET_RAW_LEN];
};

struct dropwatch_packet_event {
	struct dropwatch_packet_meta meta;
	struct dropwatch_packet_raw pkt_hdr;
	u64 stack_size;
	u64 stack[PERF_MAX_STACK_DEPTH];
};

BPF_ABI_EXPORT(dropwatch_packet_meta);
BPF_ABI_EXPORT(dropwatch_packet_raw);
BPF_ABI_EXPORT(dropwatch_packet_event);

#endif /* __BPF_ABI_DROPWATCH_H__ */

// go:build ignore

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

#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_endian.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_net_namespace.h"
#include "bpf_ratelimit.h"
#include "bpf_skbuff.h"
#include "abi/net_rx_latency_types.h"

volatile const long long txlat_thresh_sendmsg_qdisc = 50 * 1000 * 1000;
volatile const long long txlat_thresh_qdisc_dev = 10 * 1000 * 1000;
volatile const long long txlat_thresh_dev_nic = 1 * 1000 * 1000;

BPF_RATELIMIT(rate, 1, 100);

enum tx_lat_stage {
	TX_STAGE_SENDMSG_QDISC,
	TX_STAGE_QDISC_DEV,
	TX_STAGE_DEV_NIC,
};

struct tx_origin {
	u64 started_at;
	u64 tgid_pid;
	u8 comm[COMPAT_TASK_COMM_LEN];
};

struct tx_skb_state {
	struct net_rx_latency_event event;
	u64 queued_at;
	u64 xmit_started_at;
	u8 qdisc_checked;
};

struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__uint(max_entries, 16384);
	__type(key, u64);
	__type(value, struct tx_origin);
} tx_sock_origins SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 16384);
	__type(key, u64);
	__type(value, u64);
} tx_call_socks SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__uint(max_entries, 65536);
	__type(key, u64);
	__type(value, struct tx_skb_state);
} tx_skb_states SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} net_tx_lat_event_map SEC(".maps");

static __always_inline bool skb_is_ipv4_tcp(struct sk_buff *skb)
{
	struct iphdr ip_hdr;

	if (!skb || BPF_CORE_READ(skb, protocol) != bpf_htons(ETH_P_IP))
		return false;

	if (bpf_probe_read_kernel(&ip_hdr, sizeof(ip_hdr),
				  skb_network_header(skb)))
		return false;
	return ip_hdr.protocol == IPPROTO_TCP;
}

static __always_inline bool
fill_skb_state(struct tx_skb_state *state, struct sk_buff *skb,
	       const struct tx_origin *origin, u64 now)
{
	struct net_rx_latency_event *event = &state->event;
	struct iphdr ip_hdr;
	struct tcphdr tcp_hdr;
	struct net_device *dev;

	if (bpf_probe_read_kernel(&ip_hdr, sizeof(ip_hdr),
				  skb_network_header(skb)) ||
	    bpf_probe_read_kernel(&tcp_hdr, sizeof(tcp_hdr),
				  skb_transport_header(skb)))
		return false;

	event->tgid_pid = origin->tgid_pid;
	__builtin_memcpy(event->comm, origin->comm, sizeof(event->comm));
	event->packet_len_bytes = BPF_CORE_READ(skb, len);
	event->tcp_sport = tcp_hdr.source;
	event->tcp_dport = tcp_hdr.dest;
	event->tcp_saddr = ip_hdr.saddr;
	event->tcp_daddr = ip_hdr.daddr;
	event->tcp_seq = tcp_hdr.seq;
	event->tcp_ack_seq = tcp_hdr.ack_seq;
	event->tcp_state = skb_sk_state(skb);
	event->netdev_name[0] = '-';
	event->netns_inum = skb_netns_inum(skb);
	event->netns_cookie = skb_netns_cookie(skb);

	dev = BPF_CORE_READ(skb, dev);
	if (dev)
		bpf_probe_read_kernel_str(event->netdev_name,
					  sizeof(event->netdev_name), dev->name);

	state->queued_at = now;
	return true;
}

static __always_inline void
submit_txlat_event(void *ctx, const struct tx_skb_state *state, u64 latency,
		   u8 stage)
{
	struct net_rx_latency_event event;

	if (bpf_ratelimited(&rate))
		return;

	__builtin_memcpy(&event, &state->event, sizeof(event));
	event.latency_ns = latency;
	event.latency_stage = stage;
	bpf_perf_event_output(ctx, &net_tx_lat_event_map,
			      COMPAT_BPF_F_CURRENT_CPU, &event, sizeof(event));
}

SEC("kprobe/tcp_sendmsg")
int tcp_sendmsg_txlat_enter(struct pt_regs *ctx)
{
	struct sock *sk = (struct sock *)PT_REGS_PARM1_CORE(ctx);
	u64 sock_key = (u64)sk;
	u64 call_key = bpf_get_current_pid_tgid();
	struct tx_origin origin = {
		.started_at = bpf_ktime_get_ns(),
		.tgid_pid = call_key,
	};

	bpf_get_current_comm(origin.comm, sizeof(origin.comm));
	bpf_map_update_elem(&tx_sock_origins, &sock_key, &origin,
			    COMPAT_BPF_ANY);
	bpf_map_update_elem(&tx_call_socks, &call_key, &sock_key,
			    COMPAT_BPF_ANY);
	return 0;
}

SEC("kretprobe/tcp_sendmsg")
int tcp_sendmsg_txlat_exit(struct pt_regs *ctx)
{
	u64 call_key = bpf_get_current_pid_tgid();
	u64 *sock_key = bpf_map_lookup_elem(&tx_call_socks, &call_key);

	if (sock_key)
		bpf_map_delete_elem(&tx_sock_origins, sock_key);
	bpf_map_delete_elem(&tx_call_socks, &call_key);
	return 0;
}

SEC("tracepoint/net/net_dev_queue")
int net_dev_queue_txlat(struct trace_event_raw_net_dev_template *ctx)
{
	struct sk_buff *skb = (struct sk_buff *)ctx->skbaddr;
	struct sock *sk;
	u64 sock_key;
	u64 skb_key = (u64)skb;
	u64 now;
	struct tx_origin *origin;
	struct tx_skb_state state = {};

	if (!skb_is_ipv4_tcp(skb))
		return 0;

	sk = BPF_CORE_READ(skb, sk);
	sock_key = (u64)sk;
	origin = bpf_map_lookup_elem(&tx_sock_origins, &sock_key);
	if (!origin)
		return 0;

	now = bpf_ktime_get_ns();
	if (!fill_skb_state(&state, skb, origin, now))
		return 0;

	bpf_map_update_elem(&tx_skb_states, &skb_key, &state, COMPAT_BPF_ANY);
	if (now >= origin->started_at &&
	    now - origin->started_at >= txlat_thresh_sendmsg_qdisc)
		submit_txlat_event(ctx, &state, now - origin->started_at,
				   TX_STAGE_SENDMSG_QDISC);
	return 0;
}

SEC("tracepoint/net/net_dev_start_xmit")
int net_dev_start_xmit_txlat(struct trace_event_raw_net_dev_start_xmit *ctx)
{
	u64 skb_key = (u64)ctx->skbaddr;
	struct tx_skb_state *state;
	u64 now;

	state = bpf_map_lookup_elem(&tx_skb_states, &skb_key);
	if (!state)
		return 0;

	now = bpf_ktime_get_ns();
	if (!state->qdisc_checked && now >= state->queued_at) {
		u64 latency = now - state->queued_at;

		state->qdisc_checked = 1;
		if (latency >= txlat_thresh_qdisc_dev)
			submit_txlat_event(ctx, state, latency,
					   TX_STAGE_QDISC_DEV);
	}
	state->xmit_started_at = now;
	return 0;
}

SEC("tracepoint/net/net_dev_xmit")
int net_dev_xmit_txlat(struct trace_event_raw_net_dev_xmit *ctx)
{
	u64 skb_key = (u64)ctx->skbaddr;
	struct tx_skb_state *state;
	u64 now;

	state = bpf_map_lookup_elem(&tx_skb_states, &skb_key);
	if (!state)
		return 0;

	now = bpf_ktime_get_ns();
	if (state->xmit_started_at && now >= state->xmit_started_at &&
	    now - state->xmit_started_at >= txlat_thresh_dev_nic)
		submit_txlat_event(ctx, state, now - state->xmit_started_at,
				   TX_STAGE_DEV_NIC);

	/* NETDEV_TX_BUSY leaves ownership with the qdisc for a later retry. */
	if (ctx->rc == 0x10)
		state->xmit_started_at = 0;
	else
		bpf_map_delete_elem(&tx_skb_states, &skb_key);
	return 0;
}

char __license[] SEC("license") = "Dual MIT/GPL";

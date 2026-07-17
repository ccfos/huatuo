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

// go:build ignore

#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_endian.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_map.h"
#include "bpf_net_namespace.h"
#include "bpf_ratelimit.h"
#include "bpf_sock.h"
#include "vmlinux_net.h"

// Thresholds in nanoseconds, patched from userspace (config values are ms).
// TX_STAGE_SENDMSG: tcp_sendmsg -> net_dev_queue (how long the packet waits
//   from the user handing it to TCP until it reaches the device/qdisc layer).
// TX_STAGE_NIC:     net_dev_queue -> net_dev_xmit (qdisc scheduling + driver
//   + NIC transmit completion).
volatile const long long txlat_thresh_sendmsg = 50 * 1000 * 1000; // 50ms
volatile const long long txlat_thresh_nic = 1 * 1000 * 1000;	// 1ms

BPF_RATELIMIT(rate, 1, 100);

enum tx_lat_stage {
	TX_STAGE_SENDMSG,
	TX_STAGE_NIC,
};

// Mirrors net_rx_latency's perf_event_t layout byte-for-byte so the userspace
// decoder and Grafana panels stay consistent. lat_stage is reused to carry
// tx_stage. TX is IPv4-only, so addr_family is always AF_INET and only the
// low 4 bytes of the 16-byte address fields are populated.
struct perf_event_t {
	char comm[COMPAT_TASK_COMM_LEN];
	u64 latency;
	u64 tgid_pid;
	u64 pkt_len;
	u16 tcp_sport;
	u16 tcp_dport;
	u8 addr_family;
	u8 _pad1[3];
	u8 tcp_saddr[16];
	u8 tcp_daddr[16];
	u32 tcp_seq;
	u32 tcp_ack_seq;
	u8 tcp_state;
	u8 lat_stage;
	u8 _pad[2];
	char netdev_name[IFNAMSIZ];
	u32 netns_inum;
	u64 net_cookie;
};

// Carries the sender identity + the stage's anchor timestamp across the TX
// path: sock -> skb. Keeping pid/comm here lets both stage events report the
// originating process even though NIC transmit may run in softirq context.
struct tx_send_info {
	u64 ts;
	u64 tgid_pid;
	char comm[COMPAT_TASK_COMM_LEN];
};

// tcp_sendmsg entry: anchor sendmsg time + capture the sender process.
struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__type(key, struct sock *);
	__type(value, struct tx_send_info);
	__uint(max_entries, 65536);
} tx_sock_start SEC(".maps");

// net_dev_queue entry: anchor qdisc/device time, carrying the sender forward
// to the net_dev_xmit completion probe.
struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__type(key, struct sk_buff *);
	__type(value, struct tx_send_info);
	__uint(max_entries, 65536);
} tx_skb_start SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} net_tx_lat_event_map SEC(".maps");

static inline bool skb_is_ipv4_tcp(struct sk_buff *skb)
{
	if (unlikely(BPF_CORE_READ(skb, protocol) != bpf_ntohs(ETH_P_IP)))
		return false;

	struct iphdr ip_hdr;

	bpf_probe_read(&ip_hdr, sizeof(ip_hdr), skb_network_header(skb));
	return ip_hdr.protocol == IPPROTO_TCP;
}

static inline bool sk_is_ipv4_tcp(struct sock *sk)
{
	return BPF_CORE_READ(sk, __sk_common.skc_family) == AF_INET &&
	       BPF_CORE_READ_BITFIELD_PROBED(sk, sk_protocol) == IPPROTO_TCP;
}

static __always_inline void
submit_txlat_event(void *ctx, struct sk_buff *skb, u64 lat, u8 stage,
		   u64 tgid_pid, const char *comm_src)
{
	struct perf_event_t event = {};
	struct iphdr ip_hdr;
	struct tcphdr tcp_hdr;
	struct net_device *dev;

	if (bpf_ratelimited(&rate))
		return;

	bpf_probe_read(&ip_hdr, sizeof(ip_hdr), skb_network_header(skb));
	bpf_probe_read(&tcp_hdr, sizeof(tcp_hdr), skb_transport_header(skb));
	event.addr_family = AF_INET;
	event.latency = lat;
	__builtin_memcpy(event.tcp_saddr, &ip_hdr.saddr, sizeof(ip_hdr.saddr));
	__builtin_memcpy(event.tcp_daddr, &ip_hdr.daddr, sizeof(ip_hdr.daddr));
	event.tcp_sport = tcp_hdr.source;
	event.tcp_dport = tcp_hdr.dest;
	event.tcp_seq = tcp_hdr.seq;
	event.tcp_ack_seq = tcp_hdr.ack_seq;
	event.pkt_len = BPF_CORE_READ(skb, len);
	event.tcp_state = skb_sk_state(skb);
	event.lat_stage = stage;
	event.netns_inum = skb_netns_inum(skb);
	event.net_cookie = skb_netns_cookie(skb);
	event.tgid_pid = tgid_pid;
	event.netdev_name[0] = '-';

	if (comm_src)
		bpf_probe_read_kernel_str(event.comm, sizeof(event.comm),
					  comm_src);
	else
		event.comm[0] = '-';

	dev = BPF_CORE_READ(skb, dev);
	if (dev)
		bpf_probe_read_kernel_str(event.netdev_name,
					  sizeof(event.netdev_name), dev->name);

	bpf_perf_event_output(ctx, &net_tx_lat_event_map,
			      COMPAT_BPF_F_CURRENT_CPU, &event,
			      sizeof(struct perf_event_t));
}

// tcp_sendmsg(struct sock *sk, struct msghdr *msg, size_t size)
SEC("kprobe/tcp_sendmsg")
int tcp_sendmsg_prog(struct pt_regs *ctx)
{
	struct sock *sk = (struct sock *)PT_REGS_PARM1_CORE(ctx);

	if (!sk || !sk_is_ipv4_tcp(sk))
		return 0;

	struct tx_send_info info = {};

	info.ts = bpf_ktime_get_ns();
	info.tgid_pid = bpf_get_current_pid_tgid();
	bpf_get_current_comm(&info.comm, sizeof(info.comm));
	bpf_map_update_elem(&tx_sock_start, &sk, &info, BPF_ANY);
	return 0;
}

// skb is handed to the device/qdisc. Emit the sendmsg->qdisc stage if we saw
// the originating tcp_sendmsg, then anchor the NIC-stage timer on the skb.
SEC("tracepoint/net/net_dev_queue")
int net_dev_queue_prog(struct trace_event_raw_net_dev_template *args)
{
	struct sk_buff *skb = (struct sk_buff *)args->skbaddr;

	if (!skb_is_ipv4_tcp(skb))
		return 0;

	u64 now = bpf_ktime_get_ns();
	struct sock *sk = BPF_CORE_READ(skb, sk);

	struct tx_send_info kinfo = {};

	kinfo.ts = now;
	kinfo.tgid_pid = 0;
	kinfo.comm[0] = '-';

	if (sk) {
		struct tx_send_info *sinfo =
			bpf_map_lookup_elem(&tx_sock_start, &sk);

		if (sinfo) {
			u64 lat = now - sinfo->ts;

			if (lat >= txlat_thresh_sendmsg)
				submit_txlat_event(args, skb, lat,
						   TX_STAGE_SENDMSG,
						   sinfo->tgid_pid, sinfo->comm);
			kinfo.tgid_pid = sinfo->tgid_pid;
			bpf_probe_read_kernel_str(kinfo.comm,
						  sizeof(kinfo.comm),
						  sinfo->comm);
			bpf_map_delete_elem(&tx_sock_start, &sk);
		}
	}

	bpf_map_update_elem(&tx_skb_start, &skb, &kinfo, BPF_ANY);
	return 0;
}

// Driver/NIC transmit completed. Emit the qdisc->nic stage.
SEC("tracepoint/net/net_dev_xmit")
int net_dev_xmit_prog(struct trace_event_raw_net_dev_xmit *args)
{
	struct sk_buff *skb = (struct sk_buff *)args->skbaddr;

	if (!skb)
		return 0;

	struct tx_send_info *kinfo = bpf_map_lookup_elem(&tx_skb_start, &skb);

	if (!kinfo)
		return 0;

	u64 lat = bpf_ktime_get_ns() - kinfo->ts;

	bpf_map_delete_elem(&tx_skb_start, &skb);
	if (lat >= txlat_thresh_nic)
		submit_txlat_event(args, skb, lat, TX_STAGE_NIC,
				   kinfo->tgid_pid, kinfo->comm);
	return 0;
}

char __license[] SEC("license") = "Dual MIT/GPL";

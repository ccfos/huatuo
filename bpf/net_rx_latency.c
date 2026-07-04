// go:build ignore

#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_endian.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_ratelimit.h"
#include "bpf_net_namespace.h"
#include "bpf_sock.h"
#include "vmlinux_net.h"

volatile const long long mono_wall_offset = 0;
volatile const long long rxlat_thresh_netif	  = 5 * 1000 * 1000;   // 5ms
volatile const long long rxlat_thresh_tcpv4	  = 10 * 1000 * 1000;  // 10ms
volatile const long long rxlat_thresh_usercopy	  = 115 * 1000 * 1000; // 115ms

BPF_RATELIMIT(rate, 1, 100);

struct perf_event_t {
	char comm[COMPAT_TASK_COMM_LEN];
	u64 latency;
	u64 tgid_pid;
	u64 pkt_len;
	u16 sport;
	u16 dport;
	u32 saddr;
	u32 daddr;
	u32 seq;
	u32 ack_seq;
	u8 state;
	u8 where;
	u8 _pad[2];
	char netdev_name[IFNAMSIZ];
	u32 netns_inum;
};

enum rx_lat_stage {
	RX_STAGE_NETIF,
	RX_STAGE_TCPV4,
	RX_STAGE_USERCOPY,
};

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} net_recv_lat_event_map SEC(".maps");

// CO-RE flavors for the skb timestamp-type bit. Two coexisting field names across kernels:
//   - 6.0 .. 6.9 mainline + RHEL 5.14 backport (Rocky 9.6): mono_delivery_time:1
//   - 6.10+ mainline + Ubuntu 6.8 backport (24.04 latest): tstamp_type
//     (1-bit early, 2-bit later when SKB_CLOCK_TAI was added)
// Pre-6.0 kernels (e.g. Ubuntu 22.04 GA 5.15) have neither — tstamp is wallclock.
struct sk_buff___mdt { // mono_delivery_time
	__u8 mono_delivery_time : 1;
} __attribute__((preserve_access_index));

struct sk_buff___tt { // tstamp_type
	__u8 tstamp_type : 2;
} __attribute__((preserve_access_index));

// Mirrors enum skb_tstamp_type in include/linux/skbuff.h
enum skb_tstamp_type {
	SKB_CLOCK_REALTIME,
	SKB_CLOCK_MONOTONIC,
	SKB_CLOCK_TAI,
};

// skb_clock_class detects which clock domain skb->tstamp belongs to via CO-RE.
//  1  = MONOTONIC  (compare directly with bpf_ktime_get_ns())
//  0  = REALTIME   (add mono_wall_offset before comparing)
// -1  = TAI        (no usable formula; caller must skip the packet)
static inline int skb_clock_class(struct sk_buff *skb)
{
	struct sk_buff___tt *skb_tt   = (struct sk_buff___tt *)skb;
	struct sk_buff___mdt *skb_mdt = (struct sk_buff___mdt *)skb;

	if (bpf_core_field_exists(skb_tt->tstamp_type)) {
		u8 t = BPF_CORE_READ_BITFIELD_PROBED(skb_tt, tstamp_type);
		if (t == SKB_CLOCK_TAI)
			return -1;
		return (t == SKB_CLOCK_MONOTONIC) ? 1 : 0;
	}
	if (bpf_core_field_exists(skb_mdt->mono_delivery_time))
		return !!BPF_CORE_READ_BITFIELD_PROBED(skb_mdt, mono_delivery_time);
	return 0; // pre-6.0: tstamp is wallclock
}

static inline u64 delta_now_skb_tstamp(struct sk_buff *skb)
{
	u64 tstamp = BPF_CORE_READ(skb, tstamp);
	// although the skb->tstamp record is opened in user space by
	// SOF_TIMESTAMPING_RX_SOFTWARE, it is still 0 in the following cases:
	// unix recv, netlink recv, few virtual dev(e.g. tun dev, napi dsabled)
	if (!tstamp)
		return 0;

	int cls = skb_clock_class(skb);
	if (cls < 0)
		return 0; // TAI: no correct formula, skip packet

	u64 now = cls ? bpf_ktime_get_ns() : bpf_ktime_get_ns() + mono_wall_offset;
	if (tstamp > now)
		return 0;
	return now - tstamp;
}

static inline bool skb_is_ipv4_tcp(struct sk_buff *skb)
{
	if (unlikely(BPF_CORE_READ(skb, protocol) != bpf_ntohs(ETH_P_IP)))
		return false;

	struct iphdr ip_hdr;

	bpf_probe_read(&ip_hdr, sizeof(ip_hdr), skb_network_header(skb));
	return ip_hdr.protocol == IPPROTO_TCP;
}

static inline u64 skb_latency_check(struct sk_buff *skb, u64 threshold)
{
	u64 delta = delta_now_skb_tstamp(skb);
	return (delta >= threshold) ? delta : 0;
}

static inline void
submit_rxlat_event(void *ctx, struct sk_buff *skb, u64 lat, u8 where)
{
	struct perf_event_t event = {};
	struct iphdr ip_hdr;
	struct tcphdr tcp_hdr;
	struct net_device *dev;

	if (bpf_ratelimited(&rate))
		return;

	if (likely(where == RX_STAGE_USERCOPY)) {
		event.tgid_pid = bpf_get_current_pid_tgid();
		bpf_get_current_comm(&event.comm, sizeof(event.comm));
	}

	bpf_probe_read(&ip_hdr, sizeof(ip_hdr), skb_network_header(skb));
	bpf_probe_read(&tcp_hdr, sizeof(tcp_hdr), skb_transport_header(skb));
	event.latency = lat;
	event.saddr   = ip_hdr.saddr;
	event.daddr   = ip_hdr.daddr;
	event.sport   = tcp_hdr.source;
	event.dport   = tcp_hdr.dest;
	event.seq     = tcp_hdr.seq;
	event.ack_seq = tcp_hdr.ack_seq;
	event.pkt_len = BPF_CORE_READ(skb, len);
	event.state   = (where == RX_STAGE_NETIF) ? 0 : skb_sk_state(skb);
	event.where   = where;

	dev = BPF_CORE_READ(skb, dev);
	if (dev) {
		bpf_probe_read_kernel_str(
			event.netdev_name,
			sizeof(event.netdev_name),
			dev->name);
	}

	event.netns_inum = skb_netns_inum(skb);

	bpf_perf_event_output(ctx, &net_recv_lat_event_map,
			      COMPAT_BPF_F_CURRENT_CPU, &event,
			      sizeof(struct perf_event_t));
}

SEC("tracepoint/net/netif_receive_skb")
int netif_receive_skb_prog(struct trace_event_raw_net_dev_template *args)
{
	struct sk_buff *skb = (struct sk_buff *)args->skbaddr;

	if (!skb_is_ipv4_tcp(skb))
		return 0;

	u64 delta = skb_latency_check(skb, rxlat_thresh_netif);
	if (!delta)
		return 0;

	submit_rxlat_event(args, skb, delta, RX_STAGE_NETIF);
	return 0;
}

SEC("kprobe/tcp_v4_rcv")
int tcp_v4_rcv_prog(struct pt_regs *ctx)
{
	struct sk_buff *skb = (struct sk_buff *)PT_REGS_PARM1_CORE(ctx);

	u64 delta = skb_latency_check(skb, rxlat_thresh_tcpv4);
	if (!delta)
		return 0;

	submit_rxlat_event(ctx, skb, delta, RX_STAGE_TCPV4);
	return 0;
}

SEC("tracepoint/skb/skb_copy_datagram_iovec")
int skb_copy_datagram_iovec_prog(
    struct trace_event_raw_skb_copy_datagram_iovec *args)
{
	struct sk_buff *skb = (struct sk_buff *)args->skbaddr;

	if (!skb_is_ipv4_tcp(skb))
		return 0;

	u64 delta = skb_latency_check(skb, rxlat_thresh_usercopy);
	if (!delta)
		return 0;

	submit_rxlat_event(args, skb, delta, RX_STAGE_USERCOPY);
	return 0;
}

char __license[] SEC("license") = "Dual MIT/GPL";

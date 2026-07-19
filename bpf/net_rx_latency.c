// go:build ignore

#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_endian.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_net_namespace.h"
#include "bpf_ratelimit.h"
#include "bpf_sock.h"
#include "vmlinux_net.h"

volatile const long long mono_wall_offset = 0;
volatile const long long rxlat_thresh_netif = 5 * 1000 * 1000;	    // 5ms
volatile const long long rxlat_thresh_tcp = 10 * 1000 * 1000;	    // 10ms
volatile const long long rxlat_thresh_usercopy = 115 * 1000 * 1000; // 115ms
volatile const long long rxlat_thresh_iptable = 10 * 1000 * 1000;   // 10ms, ipt_do_table duration

BPF_RATELIMIT(rate, 1, 100);

// Addresses are stored as 16-byte fields so a single layout serves both
// families: IPv4 occupies the low 4 bytes (network order, rest zero), IPv6
// uses all 16. addr_family (AF_INET / AF_INET6) tells userspace how to
// format them. Mirrored byte-for-byte by net_tx_latency.c.
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

enum rx_lat_stage {
	RX_STAGE_NETIF,
	RX_STAGE_TCP,
	RX_STAGE_USERCOPY,
	// RX_STAGE_IPTABLE measures ipt_do_table() self-duration (rule matching),
	// not an skb-tstamp delta like the stages above. Appended last so the
	// existing NETIF/TCP/USERCOPY indices stay stable (Go latStageNames and
	// the volatile-const threshold order are both indexed by this enum).
	RX_STAGE_IPTABLE,
};

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} net_recv_lat_event_map SEC(".maps");

// Correlates ipt_do_table() entry with its kretprobe return to measure the
// function's own duration (slow rule evaluation on iptables-mode k8s). Keyed by
// pid_tgid: like memory_reclaim_events.c, ipt_do_table runs in softirq with
// preemption disabled around the probed call, so pid_tgid is identical at
// entry and return. The skb is stashed here because the kretprobe cannot read
// PT_REGS_PARM1 (registers are clobbered by the function body at return).
// Reentrancy caveat: a reentrant call (user-defined -j chain, TEE target)
// overwrites the outer entry; on the outer return the lookup misses and that
// measurement is dropped — no false attribution, only a lost outer sample.
struct ipt_lat_entry {
	u64 start_ns;
	struct sk_buff *skb;
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct ipt_lat_entry);
	__uint(max_entries, 10240);
} ipt_lat_map SEC(".maps");

// CO-RE flavors for the skb timestamp-type bit. Two coexisting field names
// across kernels:
//   - 6.0 .. 6.9 mainline + RHEL 5.14 backport (Rocky 9.6):
//   mono_delivery_time:1
//   - 6.10+ mainline + Ubuntu 6.8 backport (24.04 latest): tstamp_type
//     (1-bit early, 2-bit later when SKB_CLOCK_TAI was added)
// Pre-6.0 kernels (e.g. Ubuntu 22.04 GA 5.15) have neither — tstamp is
// wallclock.
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
	struct sk_buff___tt *skb_tt = (struct sk_buff___tt *)skb;
	struct sk_buff___mdt *skb_mdt = (struct sk_buff___mdt *)skb;

	if (bpf_core_field_exists(skb_tt->tstamp_type)) {
		u8 t = BPF_CORE_READ_BITFIELD_PROBED(skb_tt, tstamp_type);
		if (t == SKB_CLOCK_TAI)
			return -1;
		return (t == SKB_CLOCK_MONOTONIC) ? 1 : 0;
	}
	if (bpf_core_field_exists(skb_mdt->mono_delivery_time))
		return !!BPF_CORE_READ_BITFIELD_PROBED(skb_mdt,
						       mono_delivery_time);
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

	u64 now = cls ? bpf_ktime_get_ns()
		      : bpf_ktime_get_ns() + mono_wall_offset;
	if (tstamp > now)
		return 0;
	return now - tstamp;
}

// skb_tcp_family returns AF_INET / AF_INET6 for TCP packets, else 0, so the
// protocol-agnostic RX tracepoints accept both families. The tcp_v4_rcv /
// tcp_v6_rcv kprobes need no check — the symbol already implies the family.
static inline u8 skb_tcp_family(struct sk_buff *skb)
{
	__be16 proto = BPF_CORE_READ(skb, protocol);

	if (proto == bpf_ntohs(ETH_P_IP)) {
		struct iphdr h;

		bpf_probe_read(&h, sizeof(h), skb_network_header(skb));
		return (h.protocol == IPPROTO_TCP) ? AF_INET : 0;
	}

	if (proto == bpf_ntohs(ETH_P_IPV6)) {
		struct ipv6hdr h;

		bpf_probe_read(&h, sizeof(h), skb_network_header(skb));
		// NOTE: nexthdr check does not walk extension header chains;
		// IPv6+EH TCP packets are caught by the tcp_v6_rcv
		// kprobe instead.
		return (h.nexthdr == IPPROTO_TCP) ? AF_INET6 : 0;
	}

	return 0;
}

static inline u64 skb_latency_check(struct sk_buff *skb, u64 threshold)
{
	u64 delta = delta_now_skb_tstamp(skb);
	return (delta >= threshold) ? delta : 0;
}

static inline void
submit_rxlat_event(void *ctx, struct sk_buff *skb, u64 lat, u8 where,
		   u8 family)
{
	struct perf_event_t event = {};
	struct tcphdr tcp_hdr;
	struct net_device *dev;

	if (bpf_ratelimited(&rate))
		return;

	if (family == AF_INET6) {
		struct ipv6hdr ip6_hdr;

		bpf_probe_read(&ip6_hdr, sizeof(ip6_hdr),
			       skb_network_header(skb));
		__builtin_memcpy(event.tcp_saddr, &ip6_hdr.saddr,
				 sizeof(ip6_hdr.saddr));
		__builtin_memcpy(event.tcp_daddr, &ip6_hdr.daddr,
				 sizeof(ip6_hdr.daddr));
	} else {
		struct iphdr ip_hdr;

		bpf_probe_read(&ip_hdr, sizeof(ip_hdr),
			       skb_network_header(skb));
		__builtin_memcpy(event.tcp_saddr, &ip_hdr.saddr,
				 sizeof(ip_hdr.saddr));
		__builtin_memcpy(event.tcp_daddr, &ip_hdr.daddr,
				 sizeof(ip_hdr.daddr));
	}
	bpf_probe_read(&tcp_hdr, sizeof(tcp_hdr), skb_transport_header(skb));
	event.addr_family = family;
	event.latency = lat;
	event.tcp_sport = tcp_hdr.source;
	event.tcp_dport = tcp_hdr.dest;
	event.tcp_seq = tcp_hdr.seq;
	event.tcp_ack_seq = tcp_hdr.ack_seq;
	event.pkt_len = BPF_CORE_READ(skb, len);
	event.tcp_state = (where == RX_STAGE_NETIF) ? 0 : skb_sk_state(skb);
	event.lat_stage = where;
	event.netdev_name[0] = '-';
	event.comm[0] = '-';
	event.netns_inum = skb_netns_inum(skb);
	event.net_cookie = skb_netns_cookie(skb);
	event.tgid_pid = 0;

	if (likely(where == RX_STAGE_USERCOPY)) {
		event.tgid_pid = bpf_get_current_pid_tgid();
		bpf_get_current_comm(&event.comm, sizeof(event.comm));
	}
	dev = BPF_CORE_READ(skb, dev);
	if (dev) {
		bpf_probe_read_kernel_str(event.netdev_name,
					  sizeof(event.netdev_name), dev->name);
	}

	bpf_perf_event_output(ctx, &net_recv_lat_event_map,
			      COMPAT_BPF_F_CURRENT_CPU, &event,
			      sizeof(struct perf_event_t));
}

SEC("tracepoint/net/netif_receive_skb")
int netif_receive_skb_prog(struct trace_event_raw_net_dev_template *args)
{
	struct sk_buff *skb = (struct sk_buff *)args->skbaddr;
	u8 family = skb_tcp_family(skb);

	if (!family)
		return 0;

	u64 delta = skb_latency_check(skb, rxlat_thresh_netif);
	if (!delta)
		return 0;

	submit_rxlat_event(args, skb, delta, RX_STAGE_NETIF, family);
	return 0;
}

SEC("kprobe/tcp_v4_rcv")
int tcp_v4_rcv_prog(struct pt_regs *ctx)
{
	struct sk_buff *skb = (struct sk_buff *)PT_REGS_PARM1_CORE(ctx);

	u64 delta = skb_latency_check(skb, rxlat_thresh_tcp);
	if (!delta)
		return 0;

	submit_rxlat_event(ctx, skb, delta, RX_STAGE_TCP, AF_INET);
	return 0;
}

// IPv6 counterpart of tcp_v4_rcv. Only attached when the kernel actually
// exports the symbol (CONFIG_IPV6); the userspace loader gates it via
// HasKprobeFunction("tcp_v6_rcv") so IPv4-only kernels are unaffected.
SEC("kprobe/tcp_v6_rcv")
int tcp_v6_rcv_prog(struct pt_regs *ctx)
{
	struct sk_buff *skb = (struct sk_buff *)PT_REGS_PARM1_CORE(ctx);

	u64 delta = skb_latency_check(skb, rxlat_thresh_tcp);
	if (!delta)
		return 0;

	submit_rxlat_event(ctx, skb, delta, RX_STAGE_TCP, AF_INET6);
	return 0;
}

// ipt_do_table() is the IPv4 iptables rule-matching core (ip6table uses the
// distinct ip6t_do_table symbol; nftables uses nft_do_chain — neither is
// covered here). We measure the function's own duration (entry->ret), NOT an
// skb-tstamp delta: the goal (issue #44) is to isolate slow rule evaluation on
// iptables-mode k8s (<v1.29) with complex rulesets, which an skb-tstamp delta
// would conflate with NETIF-style receive delay. Only attached when the kernel
// exports the symbol; gated in the Go loader via HasKprobeFunction("ipt_do_table").
SEC("kprobe/ipt_do_table")
int ipt_do_table_entry_prog(struct pt_regs *ctx)
{
	struct sk_buff *skb = (struct sk_buff *)PT_REGS_PARM1_CORE(ctx);

	// keep the tracer TCP-scoped, consistent with the NETIF stage; ipt_do_table
	// is IPv4-only, so only AF_INET can match here.
	if (skb_tcp_family(skb) != AF_INET)
		return 0;

	struct ipt_lat_entry e = {
		.start_ns = bpf_ktime_get_ns(),
		.skb = skb,
	};
	u64 key = bpf_get_current_pid_tgid();
	bpf_map_update_elem(&ipt_lat_map, &key, &e, COMPAT_BPF_ANY);
	return 0;
}

SEC("kretprobe/ipt_do_table")
int ipt_do_table_ret_prog(struct pt_regs *ctx)
{
	u64 key = bpf_get_current_pid_tgid();
	struct ipt_lat_entry *e = bpf_map_lookup_elem(&ipt_lat_map, &key);
	if (!e)
		return 0;
	bpf_map_delete_elem(&ipt_lat_map, &key);

	u64 delta = bpf_ktime_get_ns() - e->start_ns;
	if (delta < rxlat_thresh_iptable)
		return 0;

	// family is always AF_INET (ipt_do_table is IPv4-only); the skb was stashed
	// at entry because the ret probe cannot read PT_REGS_PARM1.
	submit_rxlat_event(ctx, e->skb, delta, RX_STAGE_IPTABLE, AF_INET);
	return 0;
}

SEC("tracepoint/skb/skb_copy_datagram_iovec")
int skb_copy_datagram_iovec_prog(
	struct trace_event_raw_skb_copy_datagram_iovec *args)
{
	struct sk_buff *skb = (struct sk_buff *)args->skbaddr;
	u8 family = skb_tcp_family(skb);

	if (!family)
		return 0;

	u64 delta = skb_latency_check(skb, rxlat_thresh_usercopy);
	if (!delta)
		return 0;

	submit_rxlat_event(args, skb, delta, RX_STAGE_USERCOPY, family);
	return 0;
}

char __license[] SEC("license") = "Dual MIT/GPL";

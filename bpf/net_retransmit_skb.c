// go:build ignore

#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_endian.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_cgroup.h"
#include "bpf_common.h"
#include "bpf_net_namespace.h"
#include "bpf_ratelimit.h"
#include "bpf_sock.h"
#include "vmlinux_net.h"

// Cap the event rate so a retransmit storm cannot overwhelm userspace. Mirrors
// net_rx_latency's limiter; tune later via config if needed.
BPF_RATELIMIT(retrans_rate, 1, 100);

// Addresses are 16-byte fields (IPv4 = low 4 bytes network order, IPv6 = all
// 16) keyed by addr_family, exactly like net_rx_latency/net_tx_latency. Mirrored
// byte-for-byte by core/events/net_retransmit.go netRetransmitPerfEvent.
struct perf_event_t {
	u64 ktime_ns;       // 8  — bpf_ktime_get_ns, for time-range correlation
	u64 tgid_pid;       // 8
	u64 memcg_css_addr; // 8  — container attribution (memory cgroup CSS)
	u64 net_cookie;     // 8  — net namespace cookie (>= 5.14)
	u64 pkt_len;        // 8
	u32 netns_inum;     // 4  — net namespace inode (always available)
	u32 tcp_seq;        // 4
	u16 tcp_sport;      // 2  — network order
	u16 tcp_dport;      // 2  — network order
	u8  addr_family;    // 1  — AF_INET / AF_INET6
	u8  tcp_state;      // 1
	u16 _pad;           // 2
	u8  tcp_saddr[16];  // 16
	u8  tcp_daddr[16];  // 16
	char comm[COMPAT_TASK_COMM_LEN];   // 16
	char netdev_name[IFNAMSIZ];        // 16
};                                     // total 120 bytes

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} net_retrans_event_map SEC(".maps");

// skb_family returns AF_INET / AF_INET6 from skb->protocol. Unlike
// net_rx_latency's skb_tcp_family we do not re-check the inner L4 protocol:
// the kprobe is on tcp_retransmit_skb, so the packet is already known to be TCP.
static __always_inline u8 skb_family(struct sk_buff *skb)
{
	__be16 proto = BPF_CORE_READ(skb, protocol);

	if (proto == bpf_ntohs(ETH_P_IP))
		return AF_INET;
	if (proto == bpf_ntohs(ETH_P_IPV6))
		return AF_INET6;
	return 0;
}

static __always_inline void
submit_retrans_event(void *ctx, struct sk_buff *skb, u8 family)
{
	struct perf_event_t event = {};
	struct tcphdr tcp_hdr;

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

	if (bpf_ratelimited(&retrans_rate))
		return;

	event.ktime_ns = bpf_ktime_get_ns();
	event.tgid_pid = bpf_get_current_pid_tgid();
	event.memcg_css_addr = skb_memcg_css_addr(skb);
	event.net_cookie = skb_netns_cookie(skb);
	event.netns_inum = skb_netns_inum(skb);
	event.pkt_len = BPF_CORE_READ(skb, len);
	event.tcp_seq = tcp_hdr.seq;
	event.tcp_sport = tcp_hdr.source;
	event.tcp_dport = tcp_hdr.dest;
	event.addr_family = family;
	event.tcp_state = skb_sk_state(skb);
	event.netdev_name[0] = '-';
	event.comm[0] = '-';
	bpf_get_current_comm(&event.comm, sizeof(event.comm));

	struct net_device *dev = BPF_CORE_READ(skb, dev);
	if (dev)
		bpf_probe_read_kernel_str(event.netdev_name,
					  sizeof(event.netdev_name), dev->name);

	bpf_perf_event_output(ctx, &net_retrans_event_map,
			      COMPAT_BPF_F_CURRENT_CPU, &event,
			      sizeof(struct perf_event_t));
}

// tcp_retransmit_skb(struct sock *sk, struct sk_buff *skb, int segs) on modern
// kernels (2-arg form on very old ones). sk is always PARM1, skb always PARM2.
SEC("kprobe/tcp_retransmit_skb")
int tcp_retransmit_skb_prog(struct pt_regs *ctx)
{
	struct sk_buff *skb = (struct sk_buff *)PT_REGS_PARM2_CORE(ctx);
	u8 family = skb_family(skb);

	if (!family)
		return 0;

	submit_retransmit_event(ctx, skb, family);
	return 0;
}

char __license[] SEC("license") = "Dual MIT/GPL";

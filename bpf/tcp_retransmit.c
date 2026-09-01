#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_endian.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_cgroup.h"
#include "bpf_net_namespace.h"
#include "bpf_pcap_stub.h"
#include "bpf_ratelimit.h"
#include "bpf_skbuff.h"
#include "bpf_tracepoint.h"
#include "abi/tcp_retransmit_types.h"

#define TCP_WIRE_FLAGS_SYNACK 0x12

struct retransmit_filter_tcp_header {
	__be16 source;
	__be16 dest;
	__be32 seq;
	__be32 ack_seq;
	u8 data_offset;
	u8 flags;
	__be16 window;
	__sum16 check;
	__be16 urg_ptr;
};

struct retransmit_filter_ipv4_header {
	u8 version_ihl;
	u8 tos;
	__be16 total_len;
	__be16 id;
	__be16 frag_off;
	u8 ttl;
	u8 protocol;
	__sum16 check;
	__be32 saddr;
	__be32 daddr;
};

struct retransmit_filter_ipv6_header {
	__be32 version_class_flow;
	__be16 payload_len;
	u8 nexthdr;
	u8 hop_limit;
	struct in6_addr saddr;
	struct in6_addr daddr;
};

struct retransmit_filter_ipv4_packet {
	struct retransmit_filter_ipv4_header ip;
	struct retransmit_filter_tcp_header tcp;
};

struct retransmit_filter_ipv6_packet {
	struct retransmit_filter_ipv6_header ip;
	struct retransmit_filter_tcp_header tcp;
};

union retransmit_filter_packet {
	struct retransmit_filter_ipv4_packet v4;
	struct retransmit_filter_ipv6_packet v6;
};

_Static_assert(sizeof(struct retransmit_filter_tcp_header) == 20,
	       "synthetic TCP header must match the wire layout");
_Static_assert(sizeof(struct retransmit_filter_ipv4_packet) == 40,
	       "synthetic IPv4/TCP packet must contain two minimum headers");
_Static_assert(sizeof(struct retransmit_filter_ipv6_packet) == 60,
	       "synthetic IPv6/TCP packet must contain two minimum headers");

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} perf_events SEC(".maps");

BPF_RATELIMIT_IN_MAP_RC(tcp_retransmit);

char __license[] SEC("license") = "Dual MIT/GPL";

static __always_inline void init_retransmit_event(struct tcp_retransmit_event *ev,
						  u8 event_type)
{
	ev->event_type = event_type;
	ev->ktime_ns = bpf_ktime_get_ns();
	ev->tgid_pid = bpf_get_current_pid_tgid();
	bpf_get_current_comm(&ev->comm, sizeof(ev->comm));
}

static __always_inline u8 read_ca_state(struct sock *sk)
{
	struct inet_connection_sock *icsk = (struct inet_connection_sock *)sk;

	if (!sk)
		return 0;

	if (!bpf_core_field_exists(icsk->icsk_ca_state))
		return 0;

	/* icsk_ca_state is a bitfield whose width changed across kernels
	 * (6 bits before 4.20, 5 bits after); let CO-RE relocate the shift
	 * and mask instead of hardcoding them. */
	u8 ca = BPF_CORE_READ_BITFIELD_PROBED(icsk, icsk_ca_state);

	if (ca > TCP_CA_Loss)
		return 0;
	return ca;
}

struct reorder_metrics {
	u32 reord_seen;
	u32 dsack_dups;
};

static __always_inline void read_reorder_metrics(struct sock *sk,
						  struct reorder_metrics *m)
{
	if (!sk || !m)
		return;

	struct tcp_sock *tp = (struct tcp_sock *)sk;

	if (bpf_core_field_exists(((struct tcp_sock *)0)->reord_seen))
		m->reord_seen = BPF_CORE_READ(tp, reord_seen);

	if (bpf_core_field_exists(((struct tcp_sock *)0)->dsack_dups))
		m->dsack_dups = BPF_CORE_READ(tp, dsack_dups);
}

static __always_inline void fill_addrs(struct tcp_retransmit_event *ev,
				       struct sock_common *skc)
{
	if (!ev || !skc)
		return;

	if (ev->family == AF_INET) {
		if (!bpf_core_field_exists(((struct sock_common *)0)->skc_rcv_saddr))
			return;

		__be32 src = BPF_CORE_READ(skc, skc_rcv_saddr);
		__be32 dst = BPF_CORE_READ(skc, skc_daddr);
		__builtin_memcpy(ev->saddr, &src, sizeof(src));
		__builtin_memcpy(ev->daddr, &dst, sizeof(dst));
	} else if (ev->family == AF_INET6) {
		if (!bpf_core_field_exists(((struct sock_common *)0)->skc_v6_rcv_saddr))
			return;

		struct in6_addr src = {};
		struct in6_addr dst = {};
		BPF_CORE_READ_INTO(&src, skc, skc_v6_rcv_saddr);
		BPF_CORE_READ_INTO(&dst, skc, skc_v6_daddr);
		__builtin_memcpy(ev->saddr, &src, sizeof(src));
		__builtin_memcpy(ev->daddr, &dst, sizeof(dst));
	}
}

static __always_inline void read_icsk_pending(struct sock *sk,
					       struct tcp_retransmit_event *ev)
{
	if (!sk)
		return;

	struct inet_connection_sock *icsk = (struct inet_connection_sock *)sk;

	if (bpf_core_field_exists(icsk->icsk_pending))
		ev->icsk_pending = BPF_CORE_READ(icsk, icsk_pending);
}

/* TLP has no retransmission skb at this probe point. snd_nxt is the closest
 * available sequence number for the probe, while snd_una is the oldest
 * unacknowledged sequence number. */
static __always_inline void read_tlp_tcp_info(struct tcp_retransmit_event *ev,
					       struct sock *sk)
{
	struct tcp_sock *tp = (struct tcp_sock *)sk;

	if (bpf_core_field_exists(((struct tcp_sock *)0)->snd_nxt))
		ev->tcp_seq = BPF_CORE_READ(tp, snd_nxt);

	if (bpf_core_field_exists(((struct tcp_sock *)0)->snd_una))
		ev->tcp_ack = BPF_CORE_READ(tp, snd_una);
}

static __always_inline void fill_retransmit_event_from_sk(struct tcp_retransmit_event *ev,
							  struct sock *sk)
{
	if (!sk)
		return;

	ev->state = BPF_CORE_READ(sk, __sk_common.skc_state);
	ev->family = BPF_CORE_READ(sk, __sk_common.skc_family);
	ev->sport = BPF_CORE_READ(sk, __sk_common.skc_num);
	ev->dport = bpf_ntohs(BPF_CORE_READ(sk, __sk_common.skc_dport));
	ev->ca_state = read_ca_state(sk);
	if (bpf_core_field_exists(((struct inet_connection_sock *)0)->icsk_retransmits))
		ev->icsk_retransmits = BPF_CORE_READ((struct inet_connection_sock *)sk,
						      icsk_retransmits);

	ev->memcg_css_addr = sk_memcg_css_addr(sk);
	ev->netns_cookie = sk_netns_cookie(sk);
	ev->netns_inum = sk_netns_inum(sk);

	read_icsk_pending(sk, ev);

	struct reorder_metrics rm = {};
	read_reorder_metrics(sk, &rm);
	ev->reord_seen = rm.reord_seen;
	ev->dsack_dups = rm.dsack_dups;

	fill_addrs(ev, (struct sock_common *)sk);
}

static __always_inline void read_retransmit_skb_tcp_fields(struct tcp_retransmit_event *ev,
							    struct sock *sk,
							    struct sk_buff *skb)
{
	if (skb) {
		struct tcp_skb_cb *tcb =
			(struct tcp_skb_cb *)((void *)skb +
				compat_bpf_core_field_offset(((struct sk_buff *)0)->cb));

		if (bpf_core_field_exists(((struct tcp_skb_cb *)0)->seq))
			ev->tcp_seq = BPF_CORE_READ(tcb, seq);
		if (bpf_core_field_exists(((struct tcp_skb_cb *)0)->end_seq))
			ev->tcp_end_seq = BPF_CORE_READ(tcb, end_seq);
		if (bpf_core_field_exists(((struct tcp_skb_cb *)0)->tcp_flags))
			ev->tcp_flags = BPF_CORE_READ(tcb, tcp_flags);
	}

	if (sk && bpf_core_field_exists(((struct tcp_sock *)0)->rcv_nxt))
		ev->tcp_ack = BPF_CORE_READ((struct tcp_sock *)sk, rcv_nxt);
}

static __always_inline void
read_retransmit_synack_tcp_fields(struct tcp_retransmit_event *ev,
				   const struct request_sock *req)
{
	struct tcp_request_sock *treq = (struct tcp_request_sock *)req;

	if (!req)
		return;

	if (bpf_core_field_exists(((struct tcp_request_sock *)0)->snt_isn)) {
		ev->tcp_seq = BPF_CORE_READ(treq, snt_isn);
		ev->tcp_end_seq = ev->tcp_seq + 1;
	}
	if (bpf_core_field_exists(((struct tcp_request_sock *)0)->rcv_nxt))
		ev->tcp_ack = BPF_CORE_READ(treq, rcv_nxt);
	ev->tcp_flags = TCP_WIRE_FLAGS_SYNACK;
}

static __always_inline void
fill_retransmit_filter_tcp(struct retransmit_filter_tcp_header *tcp,
			   const struct tcp_retransmit_event *ev)
{
	tcp->source = bpf_htons(ev->sport);
	tcp->dest = bpf_htons(ev->dport);
	tcp->seq = bpf_htonl(ev->tcp_seq);
	tcp->ack_seq = bpf_htonl(ev->tcp_ack);
	tcp->data_offset = 5 << 4;
	tcp->flags = ev->tcp_flags;
}

static __always_inline bool
retransmit_address_is_ipv4_mapped(const u8 address[16])
{
	return address[0] == 0 && address[1] == 0 && address[2] == 0 &&
	       address[3] == 0 && address[4] == 0 && address[5] == 0 &&
	       address[6] == 0 && address[7] == 0 && address[8] == 0 &&
	       address[9] == 0 && address[10] == 0xff &&
	       address[11] == 0xff;
}

/* Retransmission queue skbs may not contain network or transport headers.
 * Build the same L3 view that dropwatch filters from the socket tuple and TCP
 * control block instead of reading unreliable skb header offsets. */
static __always_inline bool
retransmit_filter_pass(void *ctx, const struct tcp_retransmit_event *ev)
{
	union retransmit_filter_packet packet = {};

	if (ev->family == AF_INET) {
		packet.v4.ip.version_ihl = (4 << 4) | 5;
		packet.v4.ip.total_len = bpf_htons(sizeof(packet.v4));
		packet.v4.ip.ttl = 64;
		packet.v4.ip.protocol = IPPROTO_TCP;
		__builtin_memcpy(&packet.v4.ip.saddr, ev->saddr,
				 sizeof(packet.v4.ip.saddr));
		__builtin_memcpy(&packet.v4.ip.daddr, ev->daddr,
				 sizeof(packet.v4.ip.daddr));
		fill_retransmit_filter_tcp(&packet.v4.tcp, ev);
		return pcap_stub_pass_l3(
			ctx, &packet.v4,
			(void *)&packet.v4 + sizeof(packet.v4));
	}

	if (ev->family == AF_INET6) {
		bool source_is_mapped =
			retransmit_address_is_ipv4_mapped(ev->saddr);
		bool destination_is_mapped =
			retransmit_address_is_ipv4_mapped(ev->daddr);

		if (source_is_mapped != destination_is_mapped) {
			pcap_stub_pass_l2(ctx, &packet.v4, &packet.v4);
			return false;
		}

		/* Keep the raw AF_INET6 event intact; only its filter view is IPv4. */
		if (source_is_mapped) {
			packet.v4.ip.version_ihl = (4 << 4) | 5;
			packet.v4.ip.total_len = bpf_htons(sizeof(packet.v4));
			packet.v4.ip.ttl = 64;
			packet.v4.ip.protocol = IPPROTO_TCP;
			__builtin_memcpy(&packet.v4.ip.saddr, &ev->saddr[12],
					 sizeof(packet.v4.ip.saddr));
			__builtin_memcpy(&packet.v4.ip.daddr, &ev->daddr[12],
					 sizeof(packet.v4.ip.daddr));
			fill_retransmit_filter_tcp(&packet.v4.tcp, ev);
			return pcap_stub_pass_l3(
				ctx, &packet.v4,
				(void *)&packet.v4 + sizeof(packet.v4));
		}

		packet.v6.ip.version_class_flow = bpf_htonl(6U << 28);
		packet.v6.ip.payload_len = bpf_htons(sizeof(packet.v6.tcp));
		packet.v6.ip.nexthdr = IPPROTO_TCP;
		packet.v6.ip.hop_limit = 64;
		__builtin_memcpy(&packet.v6.ip.saddr, ev->saddr,
				 sizeof(packet.v6.ip.saddr));
		__builtin_memcpy(&packet.v6.ip.daddr, ev->daddr,
				 sizeof(packet.v6.ip.daddr));
		fill_retransmit_filter_tcp(&packet.v6.tcp, ev);
		return pcap_stub_pass_l3(
			ctx, &packet.v6,
			(void *)&packet.v6 + sizeof(packet.v6));
	}

	/* Keep both stubs linked for the shared loader without exposing an
	 * invalid packet view for an unsupported address family. */
	pcap_stub_pass_l2(ctx, &packet.v4, &packet.v4);
	return false;
}

SEC("tracepoint/tcp/tcp_retransmit_skb")
int retrans_skb(struct trace_event_raw_tcp_event_sk_skb_compat *ctx)
{
	struct sk_buff *skb = (struct sk_buff *)ctx->skbaddr;
	struct sock *sk = (struct sock *)ctx->skaddr;

	if (!skb || !sk)
		return 0;

	struct tcp_retransmit_event ev = {};

	init_retransmit_event(&ev, TCP_RETRANSMIT_EVENT_SKB);

	ev.skb_addr = (u64)(unsigned long)skb;
	fill_retransmit_event_from_sk(&ev, sk);

	read_retransmit_skb_tcp_fields(&ev, sk, skb);
	if (!retransmit_filter_pass(ctx, &ev))
		return 0;
	if (bpf_ratelimited_in_map_rc(ctx, tcp_retransmit))
		return 0;

	bpf_perf_event_output(ctx, &perf_events, COMPAT_BPF_F_CURRENT_CPU, &ev,
			      sizeof(ev));
	return 0;
}

SEC("tracepoint/tcp/tcp_retransmit_synack")
int retrans_synack(struct trace_event_raw_tcp_retransmit_synack *ctx)
{
	struct sock *sk = (struct sock *)ctx->skaddr;
	struct request_sock *req = (struct request_sock *)ctx->req;

	if (!sk && !req)
		return 0;

	struct tcp_retransmit_event ev = {};

	init_retransmit_event(&ev, TCP_RETRANSMIT_EVENT_SYNACK);

	if (sk) {
		ev.family = BPF_CORE_READ(sk, __sk_common.skc_family);
		ev.sport = BPF_CORE_READ(sk, __sk_common.skc_num);
		ev.memcg_css_addr = sk_memcg_css_addr(sk);
		ev.netns_cookie = sk_netns_cookie(sk);
		ev.netns_inum = sk_netns_inum(sk);
	}

	if (req) {
		if (!ev.family)
			ev.family = BPF_CORE_READ(req, __req_common.skc_family);
		ev.dport = bpf_ntohs(BPF_CORE_READ(req, __req_common.skc_dport));
		if (bpf_core_field_exists(((struct request_sock *)0)->num_retrans))
			ev.icsk_retransmits = BPF_CORE_READ(req, num_retrans);
	}

	ev.state = TCP_NEW_SYN_RECV;
	ev.skb_addr = 0;
	ev.ca_state = 0;

	fill_addrs(&ev, (struct sock_common *)req);
	read_retransmit_synack_tcp_fields(&ev, req);
	if (!retransmit_filter_pass(ctx, &ev))
		return 0;
	if (bpf_ratelimited_in_map_rc(ctx, tcp_retransmit))
		return 0;

	bpf_perf_event_output(ctx, &perf_events, COMPAT_BPF_F_CURRENT_CPU, &ev,
			      sizeof(ev));
	return 0;
}

SEC("kprobe/tcp_send_loss_probe")
int retrans_tlp(struct pt_regs *ctx)
{
	struct tcp_retransmit_event ev = {};
	struct sock *sk = (struct sock *)PT_REGS_PARM1_CORE(ctx);

	if (!sk)
		return 0;

	init_retransmit_event(&ev, TCP_RETRANSMIT_EVENT_TLP);

	fill_retransmit_event_from_sk(&ev, sk);
	read_tlp_tcp_info(&ev, sk);
	if (!retransmit_filter_pass(ctx, &ev))
		return 0;
	if (bpf_ratelimited_in_map_rc(ctx, tcp_retransmit))
		return 0;

	bpf_perf_event_output(ctx, &perf_events, COMPAT_BPF_F_CURRENT_CPU, &ev,
			      sizeof(ev));
	return 0;
}

#include "vmlinux.h"

#include <bpf/bpf_endian.h>

#include "bpf_cgroup.h"
#include "bpf_common.h"
#include "bpf_net_namespace.h"
#include "bpf_netdevice.h"
#include "bpf_pcap_stub.h"
#include "bpf_ratelimit.h"
#include "bpf_skb_filter.h"
#include "vmlinux_net.h"

#define TYPE_TCP_COMMON_DROP 1
#define TYPE_TCP_SYN_FLOOD 2
#define TYPE_TCP_LISTEN_OVERFLOW_HANDSHAKE1 3
#define TYPE_TCP_LISTEN_OVERFLOW_HANDSHAKE3 4

#define SK_FL_PROTO_SHIFT 8
#define SK_FL_PROTO_MASK 0x0000ff00
#define SK_FL_TYPE_SHIFT 16
#define SK_FL_TYPE_MASK 0xffff0000

/* Reserved for future kernel skb_drop_reason (SKB_DROP_REASON_NOT_SPECIFIED,
 * ...) passthrough; (u32)-1 is out of band: kernel reason values grow upward
 * from 0 (low 16 bits code, high 16 bits subsystem since v6.4) and can never
 * reach it. */
#define SKB_DROP_REASON_UNSUPPORT ((u32)-1)
#define PKT_RAW_LEN 120

struct packet_meta {
	u64 ktime_ns;            /* 8  */
	u64 tgid_pid;            /* 8  */
	u64 net_cookie;          /* 8  */
	u64 kfree_skb_addr;      /* 8  */
	u64 memcg_css_addr;           /* 8  */
	u32 ifindex;             /* 4  */
	u32 dev_flags;           /* 4  */
	u32 queue_mapping;       /* 4  */
	u32 drop_reason;         /* 4  */
	u32 type;                /* 4  */
	u32 net_inum;             /* 4  */
	u8  dev_name[IFNAMSIZ];  /* 16 */
	u8  comm[COMPAT_TASK_COMM_LEN]; /* 16 */
};                           /* total: 96 bytes */

struct packet_raw {
	u16 eth_proto;    /* 2  */
	u16 raw_len;      /* 2  */
	u16 has_eth_hdr;  /* 2: raw[] starts with Ethernet header; 0: starts at L3 */
	u16 pad;          /* 2  */
	u32 pkt_len;      /* 4  */
	u32 sk_state;     /* 4  */
	u8  raw[PKT_RAW_LEN]; /* PKT_RAW_LEN */
};                    /* total: 136 bytes */

struct drop_packet_event {
	struct packet_meta meta;
	struct packet_raw pkt_hdr;
	u64 stack_size;
	u64 stack[PERF_MAX_STACK_DEPTH];
};


struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} perf_events SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(max_entries, 1);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(struct drop_packet_event));
} dropwatch_stackmap SEC(".maps");

/* Runtime-configurable rate limiter. Userspace patches the three
 * bpf_rlimit_*_dropwatch constants via RewriteConstants; interval == 0
 * (default) keeps the limiter disabled with a single-load fast path.
 */
BPF_RATELIMIT_IN_MAP_RC(dropwatch);

char __license[] SEC("license") = "Dual MIT/GPL";

static const struct drop_packet_event zero_data = {};
static const u32 stackmap_key = 0;

struct sock___5_10 {
	u16 sk_type;
	u16 sk_protocol;
} __attribute__((preserve_access_index));

static void sk_get_type_and_protocol(struct sock *sk, u16 *protocol, u16 *type)
{
	// kernel version <= 4.18
	//
	// struct sock {
	//      unsigned int        __sk_flags_offset[0];
	// #ifdef __BIG_ENDIAN_BITFIELD
	// #define SK_FL_PROTO_SHIFT  16
	// #define SK_FL_PROTO_MASK   0x00ff0000
	// #
	// #define SK_FL_TYPE_SHIFT   0
	// #define SK_FL_TYPE_MASK    0x0000ffff
	// #else
	// #define SK_FL_PROTO_SHIFT  8
	// #define SK_FL_PROTO_MASK   0x0000ff00
	// #
	// #define SK_FL_TYPE_SHIFT   16
	// #define SK_FL_TYPE_MASK    0xffff0000
	// #endif
	//
	//  unsigned int        sk_padding : 1,
	//              sk_kern_sock : 1,
	//              sk_no_check_tx : 1,
	//              sk_no_check_rx : 1,
	//              sk_userlocks : 4,
	//              sk_protocol  : 8,
	//              sk_type      : 16;
	// }
	if (bpf_core_field_exists(sk->__sk_flags_offset)) {
		u32 sk_flags;

		bpf_probe_read(&sk_flags, sizeof(sk_flags),
			       &sk->__sk_flags_offset);
		*protocol = sk_flags >> SK_FL_PROTO_SHIFT;
		*type	  = sk_flags >> SK_FL_TYPE_SHIFT;
		return;
	}

	// struct sock {
	//   u16         sk_type;
	//   u16         sk_protocol;
	// }
	struct sock___5_10 *sk_new = (struct sock___5_10 *)sk;

	*protocol = BPF_CORE_READ(sk_new, sk_protocol);
	*type	  = BPF_CORE_READ(sk_new, sk_type);
	return;
}

static inline void skb_load_packet_raw(struct sk_buff *skb,
					struct packet_raw *pkt_raw,
					u16 skb_protocol)
{
	unsigned char *hdr;

	if (skb_protocol != ETH_P_IP && skb_protocol != ETH_P_IPV6 &&
	    skb_protocol != ETH_P_ARP)
		return;

	pkt_raw->eth_proto = skb_protocol;

	if (BPF_CORE_READ(skb, mac_len) > 0) {
		hdr = skb_mac_header(skb);
		pkt_raw->has_eth_hdr = 1;
	} else {
		hdr = skb_network_header(skb);
	}

	if (bpf_probe_read(pkt_raw->raw, PKT_RAW_LEN, hdr) < 0)
		return;

	pkt_raw->raw_len = PKT_RAW_LEN;
}

SEC("tracepoint/skb/kfree_skb")
int bpf_kfree_skb_prog(struct trace_event_raw_kfree_skb *ctx)
{
	struct sk_buff *skb = ctx->skbaddr;
	struct drop_packet_event *data = NULL;
	struct net_device *dev;
	u16 skb_protocol;

	/* skb->protocol is __be16 regardless of kernel version; ctx->protocol is
	 * ntohs(skb->protocol) on kernels >=5.17 but raw __be16 on older ones.
	 * Read directly from skb to avoid the ambiguity. */
	skb_protocol = bpf_ntohs(BPF_CORE_READ(skb, protocol));

	/* device filter: filter_dev_mode is rewritten at load time and
	 * skb_filter_dev_map / skb_filter_dev_excluded_map are populated from
	 * userspace. filter_dev_mode == 0 (default) means all devices pass;
	 * cheap check, runs before the pcap bytecode below.
	 */
	if (!skb_filter_pass_dev(skb))
		return 0;

	/* pcap filter via bpf_pcap_stub.h: pass-through stub patched at load
	 * time by internal/pcapinject with the compiled tcpdump expression.
	 */
	if (!PCAP_STUB_PASS_SKB(skb))
		return 0;

	/* Cap emission rate after all filters have passed, so the budget is
	 * spent on events the user actually asked for. Overflow notifications
	 * are emitted via event_bpf_rlimit_dropwatch (first miss per window).
	 */
	if (bpf_ratelimited_in_map_rc(ctx, dropwatch))
		return 0;

	data = bpf_map_lookup_elem(&dropwatch_stackmap, &stackmap_key);
	if (!data)
		return 0;

	/* meta */
	data->meta.ktime_ns = bpf_ktime_get_ns();
	data->meta.tgid_pid = bpf_get_current_pid_tgid();
	bpf_get_current_comm(&data->meta.comm, sizeof(data->meta.comm));
	data->meta.kfree_skb_addr = (u64)(unsigned long)ctx->location;
	data->meta.queue_mapping = BPF_CORE_READ(skb, queue_mapping);
	data->meta.drop_reason = SKB_DROP_REASON_UNSUPPORT;
	data->meta.type = 0;

	data->pkt_hdr.pkt_len = BPF_CORE_READ(skb, len);

	/* sk state and memory cgroup CSS */
	struct sock *sk = BPF_CORE_READ(skb, sk);
	if (sk) {
		u16 sk_protocol = 0, sk_type = 0;

		sk_get_type_and_protocol(sk, &sk_protocol, &sk_type);
		if ((u8)sk_protocol == IPPROTO_TCP && sk_type == SOCK_STREAM &&
		    BPF_CORE_READ(sk, __sk_common.skc_family) == AF_INET)
			data->pkt_hdr.sk_state = BPF_CORE_READ(sk, __sk_common.skc_state);
	}
	data->meta.memcg_css_addr = skb_memcg_css_addr(skb);

	/* net cookie and net namespace inode from device or socket */
	data->meta.net_cookie = skb_netns_cookie(skb);
	data->meta.net_inum = skb_netns_inum(skb);

	/* device info */
	data->meta.dev_name[0] = '-';
	dev = BPF_CORE_READ(skb, dev);
	if (dev) {
		data->meta.dev_flags = netif_get_flags(dev);
		data->meta.ifindex = BPF_CORE_READ(dev, ifindex);
		bpf_probe_read_str(&data->meta.dev_name,
					  sizeof(data->meta.dev_name),
					  dev->name);
	}

	/* raw packet bytes; includes Ethernet header when mac_len > 0 */
	skb_load_packet_raw(skb, &data->pkt_hdr, skb_protocol);

	/* kernel stack */
	data->stack_size = bpf_get_stack(ctx, data->stack, sizeof(data->stack), 0);

	bpf_perf_event_output(ctx, &perf_events, COMPAT_BPF_F_CURRENT_CPU, data,
			      sizeof(*data));

	bpf_map_update_elem(&dropwatch_stackmap, &stackmap_key, &zero_data,
			    COMPAT_BPF_EXIST);
	return 0;
}

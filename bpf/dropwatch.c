#include "vmlinux.h"

#include "bpf_common.h"
#include "bpf_net_namespace.h"
#include "bpf_netdevice.h"
#include "bpf_ratelimit.h"
#include "vmlinux_net.h"

#define TYPE_TCP_COMMON_DROP 1
#define TYPE_TCP_SYN_FLOOD 2
#define TYPE_TCP_LISTEN_OVERFLOW_HANDSHAKE1 3
#define TYPE_TCP_LISTEN_OVERFLOW_HANDSHAKE3 4

#define SK_FL_PROTO_SHIFT 8
#define SK_FL_PROTO_MASK 0x0000ff00
#define SK_FL_TYPE_SHIFT 16
#define SK_FL_TYPE_MASK 0xffff0000

struct perf_event_t {
	u64 tgid_pid;
	u32 saddr;
	u32 daddr;
	u16 sport;
	u16 dport;
	u32 seq;
	u32 ack_seq;
	u32 pkt_len;
	s64 stack_size;
	u64 stack[PERF_MAX_STACK_DEPTH];
	u32 queue_mapping;
	u32 dev_flags;
	u8 dev_name[IFNAMSIZ];
	u32 ifindex;
	u32 sk_max_ack_backlog;
	u8 sk_state;
	u8 type;
	u16 pad0;
	u32 pad1;
	u64 net_cookie;
	char comm[COMPAT_TASK_COMM_LEN];
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
	__uint(value_size, sizeof(struct perf_event_t));
} dropwatch_stackmap SEC(".maps");

char __license[] SEC("license") = "Dual MIT/GPL";

static const struct perf_event_t zero_data = {};
static const u32 stackmap_key		   = 0;

BPF_RATELIMIT(rate, 1, 100); // 100/s

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

SEC("tracepoint/skb/kfree_skb")
int bpf_kfree_skb_prog(struct trace_event_raw_kfree_skb *ctx)
{
	struct sk_buff *skb	  = ctx->skbaddr;
	struct perf_event_t *data = NULL;
	struct sock_common *sk_common;
	struct net_device *dev;
	struct tcphdr tcphdr;
	struct iphdr iphdr;
	struct sock *sk;
	u16 protocol = 0;
	u16 type     = 0;
	u8 sk_state  = 0;

	/* only for IP && TCP */
	if (ctx->protocol != ETH_P_IP)
		return 0;

	bpf_probe_read(&iphdr, sizeof(iphdr), skb_network_header(skb));
	if (iphdr.protocol != IPPROTO_TCP)
		return 0;

	sk = BPF_CORE_READ(skb, sk);
	if (!sk)
		return 0;

	sk_common = (struct sock_common *)sk;

	// filter the sock by AF_INET, SOCK_STREAM, IPPROTO_TCP
	if (BPF_CORE_READ(sk_common, skc_family) != AF_INET)
		return 0;

	sk_get_type_and_protocol(sk, &protocol, &type);
	if ((u8)protocol != IPPROTO_TCP || type != SOCK_STREAM)
		return 0;

	sk_state = BPF_CORE_READ(sk_common, skc_state);
	if (sk_state == TCP_CLOSE || sk_state == 0)
		return 0;

	if (bpf_ratelimited(&rate))
		return 0;

	data = bpf_map_lookup_elem(&dropwatch_stackmap, &stackmap_key);
	if (!data) {
		return 0;
	}

	bpf_probe_read(&tcphdr, sizeof(tcphdr), skb_transport_header(skb));

	/* event */
	data->tgid_pid = bpf_get_current_pid_tgid();
	bpf_get_current_comm(&data->comm, sizeof(data->comm));
	data->type		 = TYPE_TCP_COMMON_DROP;
	data->sk_state		 = sk_state;
	data->saddr		 = iphdr.saddr;
	data->daddr		 = iphdr.daddr;
	data->sport		 = tcphdr.source;
	data->dport		 = tcphdr.dest;
	data->seq		 = tcphdr.seq;
	data->ack_seq		 = tcphdr.ack_seq;
	data->pkt_len		 = BPF_CORE_READ(skb, len);
	data->queue_mapping	 = BPF_CORE_READ(skb, queue_mapping);
	data->sk_max_ack_backlog = 0;
	data->dev_name[0]	 = '-';
	data->dev_flags		 = 0;
	data->ifindex		 = 0;
	data->net_cookie	 = net_get_netns_cookie(skb);
	data->stack_size =
		bpf_get_stack(ctx, data->stack, sizeof(data->stack), 0);

	dev = BPF_CORE_READ(skb, dev);
	if (dev) {
		data->dev_flags = netif_get_flags(dev);
		data->ifindex	= BPF_CORE_READ(dev, ifindex);
		bpf_probe_read_kernel_str(&data->dev_name,
					  sizeof(data->dev_name), dev->name);
	}

	bpf_perf_event_output(ctx, &perf_events, COMPAT_BPF_F_CURRENT_CPU, data,
			      sizeof(*data));

	bpf_map_update_elem(&dropwatch_stackmap, &stackmap_key, &zero_data,
			    COMPAT_BPF_EXIST);
	return 0;
}

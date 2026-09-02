#include "vmlinux.h"

#include <bpf/bpf_endian.h>

#include "bpf_cgroup.h"
#include "bpf_common.h"
#include "bpf_net_namespace.h"
#include "bpf_netdevice.h"
#include "bpf_pcap_stub.h"
#include "bpf_perf_output.h"
#include "bpf_ratelimit.h"
#include "bpf_skb_filter.h"
#include "bpf_skbuff.h"
#include "abi/dropwatch_types.h"

#define TYPE_TCP_COMMON_DROP 1
#define TYPE_TCP_SYN_FLOOD 2
#define TYPE_TCP_LISTEN_OVERFLOW_HANDSHAKE1 3
#define TYPE_TCP_LISTEN_OVERFLOW_HANDSHAKE3 4

#define SK_FL_PROTO_SHIFT 8
#define SK_FL_PROTO_MASK 0x0000ff00
#define SK_FL_TYPE_SHIFT 16
#define SK_FL_TYPE_MASK 0xffff0000

#define HARDWARE_DROP_DEDUP_WINDOW_NS 1000000000ULL

enum devlink_trap_type___local {
	DEVLINK_TRAP_TYPE_DROP___local,
	DEVLINK_TRAP_TYPE_EXCEPTION___local,
	DEVLINK_TRAP_TYPE_CONTROL___local,
};

struct devlink_trap_metadata___local {
	const char *trap_name;
	const char *trap_group_name;
	struct net_device *input_dev;
	enum devlink_trap_type___local trap_type;
} __attribute__((preserve_access_index));

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} perf_events SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(max_entries, 1);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(struct dropwatch_packet_event));
} dropwatch_stackmap SEC(".maps");

/* Per-CPU count of events that bpf_perf_event_output failed to deliver;
 * updated by bpf_perf_event_output_counted below. */
BPF_PERF_OUTPUT_IN_MAP(dropwatch);

struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__uint(max_entries, 4096);
	__type(key, u64);
	__type(value, u64);
} dropwatch_hardware_skb SEC(".maps");

/* Runtime-configurable rate limiter. Userspace patches the three
 * bpf_rlimit_*_dropwatch constants via RewriteConstants; interval == 0
 * (default) keeps the limiter disabled with a single-load fast path.
 */
BPF_RATELIMIT_IN_MAP_RC(dropwatch);

char __license[] SEC("license") = "Dual MIT/GPL";

static const struct dropwatch_packet_event zero_data = {};
static const u32 stackmap_key = 0;

/* kfree_skb gained an skb drop reason field in v5.17, absent from the BTF this
 * object is compiled against. Carry it in a CO-RE flavor relocated at load time:
 * the anonymous enum field matches the kernel's enum skb_drop_reason by the name
 * "reason". No reason constants are hardcoded (their values shift across kernels);
 * names are resolved at runtime from kernel BTF in userspace (loadDropReasonNames).
 * SKB_DROP_REASON_NOT_SUPPORTED is the out-of-band fallback for kernels
 * predating the field. */
struct trace_event_raw_kfree_skb___reason {
	enum { SKB_DROP_REASON_NOT_SUPPORTED = -1 } reason;
} __attribute__((preserve_access_index));

/* Return the kernel skb drop reason when the running kernel supports it,
 * otherwise the out-of-band SKB_DROP_REASON_NOT_SUPPORTED sentinel. */
static inline u32 skb_get_drop_reason(struct trace_event_raw_kfree_skb *ctx)
{
	struct trace_event_raw_kfree_skb___reason *ctx_reason = (void *)ctx;

	if (bpf_core_field_exists(ctx_reason->reason))
		return BPF_CORE_READ(ctx_reason, reason);

	return SKB_DROP_REASON_NOT_SUPPORTED;
}

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
					struct dropwatch_packet_raw *pkt_raw,
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

	if (bpf_probe_read(pkt_raw->raw, DROPWATCH_PACKET_RAW_LEN, hdr) < 0)
		return;

	pkt_raw->raw_len = DROPWATCH_PACKET_RAW_LEN;
}

static __always_inline bool
dropwatch_skip_hardware_duplicate(struct sk_buff *skb)
{
	u64 skb_addr = (u64)(unsigned long)skb;
	u64 now;
	u64 timestamp;
	u64 *reported_at;

	reported_at = bpf_map_lookup_elem(&dropwatch_hardware_skb, &skb_addr);
	if (!reported_at)
		return false;

	timestamp = *reported_at;
	bpf_map_delete_elem(&dropwatch_hardware_skb, &skb_addr);
	now = bpf_ktime_get_ns();
	return now >= timestamp &&
	       now - timestamp <= HARDWARE_DROP_DEDUP_WINDOW_NS;
}

static __always_inline bool
drop_event_filter_pass(struct sk_buff *skb, struct net_device *dev)
{
	return skb_filter_pass_netdev(dev) && PCAP_STUB_PASS_SKB(skb);
}

static __always_inline int
drop_event_commit(void *ctx, struct sk_buff *skb, struct net_device *dev,
	       u32 source, u64 drop_location, u32 drop_reason,
	       const char *trap_name, const char *trap_group_name)
{
	struct dropwatch_packet_event *data;
	struct sock *sk;
	u16 skb_protocol;
	long output_ret;

	u64 event_ktime = bpf_ktime_get_ns();
	/* skb->protocol is __be16 on every supported kernel. */
	skb_protocol = bpf_ntohs(BPF_CORE_READ(skb, protocol));

	if (bpf_ratelimited_in_map_rc(ctx, dropwatch)) {
		return 0;
	}

	data = bpf_map_lookup_elem(&dropwatch_stackmap, &stackmap_key);
	if (!data)
		return 0;

	data->meta.ktime_ns = event_ktime;
	data->meta.tgid_pid = bpf_get_current_pid_tgid();
	bpf_get_current_comm(&data->meta.comm, sizeof(data->meta.comm));
	data->meta.skb_addr = (u64)(unsigned long)skb;
	data->meta.drop_location = drop_location;
	data->meta.queue_mapping = BPF_CORE_READ(skb, queue_mapping);
	data->meta.drop_reason = drop_reason;
	data->meta.drop_source = source;

	if (trap_name)
		bpf_probe_read_kernel_str(&data->meta.trap_name,
					  sizeof(data->meta.trap_name), trap_name);
	if (trap_group_name)
		bpf_probe_read_kernel_str(&data->meta.trap_group_name,
					  sizeof(data->meta.trap_group_name),
					  trap_group_name);

	data->pkt_hdr.packet_len_bytes = BPF_CORE_READ(skb, len);

	sk = BPF_CORE_READ(skb, sk);
	if (sk) {
		u16 sk_protocol = 0, sk_type = 0;

		sk_get_type_and_protocol(sk, &sk_protocol, &sk_type);
		if ((u8)sk_protocol == IPPROTO_TCP && sk_type == SOCK_STREAM &&
		    BPF_CORE_READ(sk, __sk_common.skc_family) == AF_INET)
			data->pkt_hdr.sk_state =
				BPF_CORE_READ(sk, __sk_common.skc_state);
	}
	data->meta.memcg_css_addr = skb_memcg_css_addr(skb);
	data->meta.netns_cookie = skb_netns_cookie(skb);
	data->meta.netns_inum = skb_netns_inum(skb);

	data->meta.dev_name[0] = '-';
	if (dev) {
		data->meta.dev_flags = netif_get_flags(dev);
		data->meta.ifindex = BPF_CORE_READ(dev, ifindex);
		bpf_probe_read_kernel_str(&data->meta.dev_name,
					  sizeof(data->meta.dev_name), dev->name);
	}

	skb_load_packet_raw(skb, &data->pkt_hdr, skb_protocol);
	data->stack_size = bpf_get_stack(ctx, data->stack, sizeof(data->stack), 0);

	output_ret = bpf_perf_event_output_counted(ctx, &perf_events,
						   &bpf_perf_out_dropwatch,
						   COMPAT_BPF_F_CURRENT_CPU,
						   data, sizeof(*data));
	if (output_ret >= 0 && source == DROPWATCH_DROP_SOURCE_HARDWARE) {
		u64 skb_addr = data->meta.skb_addr;
		u64 reported_at = data->meta.ktime_ns;

		bpf_map_update_elem(&dropwatch_hardware_skb, &skb_addr,
				    &reported_at, COMPAT_BPF_ANY);
	}

	bpf_map_update_elem(&dropwatch_stackmap, &stackmap_key, &zero_data,
			    COMPAT_BPF_EXIST);
	return 0;
}

SEC("tracepoint/skb/kfree_skb")
int bpf_kfree_skb_prog(struct trace_event_raw_kfree_skb *ctx)
{
	struct sk_buff *skb = ctx->skbaddr;
	struct net_device *dev;

	/* A rejected software event must not leave a marker for a reused address. */
	if (dropwatch_skip_hardware_duplicate(skb))
		return 0;

	dev = BPF_CORE_READ(skb, dev);
	if (!drop_event_filter_pass(skb, dev))
		return 0;

	return drop_event_commit(ctx, skb, dev, DROPWATCH_DROP_SOURCE_SOFTWARE,
			      (u64)(unsigned long)ctx->location,
			      skb_get_drop_reason(ctx), NULL, NULL);
}

SEC("raw_tracepoint/devlink_trap_report")
int bpf_devlink_trap_report_prog(struct bpf_raw_tracepoint_args *ctx)
{
	struct sk_buff *skb = (void *)ctx->args[1];
	struct devlink_trap_metadata___local *metadata =
		(void *)ctx->args[2];
	struct net_device *dev;
	const char *trap_name;
	const char *trap_group_name;

	if (!skb || !metadata)
		return 0;

	if (BPF_CORE_READ(metadata, trap_type) !=
	    DEVLINK_TRAP_TYPE_DROP___local)
		return 0;

	dev = BPF_CORE_READ(metadata, input_dev);
	if (!dev)
		dev = BPF_CORE_READ(skb, dev);
	if (!drop_event_filter_pass(skb, dev))
		return 0;

	trap_name = BPF_CORE_READ(metadata, trap_name);
	trap_group_name = BPF_CORE_READ(metadata, trap_group_name);

	return drop_event_commit(ctx, skb, dev, DROPWATCH_DROP_SOURCE_HARDWARE, 0,
			      0, trap_name, trap_group_name);
}

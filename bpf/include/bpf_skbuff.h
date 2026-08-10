#ifndef __BPF_SKBUFF_H__
#define __BPF_SKBUFF_H__

#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

#define IFNAMSIZ    16

#define ETH_P_IP    0x0800
#define ETH_P_IPV6  0x86DD
#define ETH_P_ARP   0x0806
#define AF_INET     2
#define AF_INET6    10
#define IPPROTO_TCP 6
#define TCP_CLOSE   7

#define IP_MF       0x2000
#define IP_OFFSET   0x1FFF

static __always_inline unsigned char *skb_mac_header(struct sk_buff *skb)
{
	return BPF_CORE_READ(skb, head) + BPF_CORE_READ(skb, mac_header);
}

static __always_inline unsigned char *skb_network_header(struct sk_buff *skb)
{
	return BPF_CORE_READ(skb, head) + BPF_CORE_READ(skb, network_header);
}

static __always_inline unsigned char *skb_transport_header(struct sk_buff *skb)
{
	return BPF_CORE_READ(skb, head) + BPF_CORE_READ(skb, transport_header);
}

static __always_inline u32 skb_l3_len(struct sk_buff *skb)
{
	unsigned char *head = BPF_CORE_READ(skb, head);
	unsigned char *data = BPF_CORE_READ(skb, data);
	u32 network_offset = BPF_CORE_READ(skb, network_header);
	u64 data_offset;
	u64 packet_end;

	if (!head || !data || network_offset == 0xffff)
		return 0;

	data_offset = (u64)data - (u64)head;
	packet_end = data_offset + BPF_CORE_READ(skb, len);
	if (packet_end < network_offset ||
	    packet_end - network_offset > 0xffffffff)
		return 0;

	/* skb->len starts at skb->data, which may precede or follow the network
	 * header depending on the drop point. */
	return packet_end - network_offset;
}

static __always_inline bool
skb_tcp_header(struct sk_buff *skb, struct tcphdr *tcp_hdr)
{
	if (!skb || !tcp_hdr)
		return false;

	return bpf_probe_read(tcp_hdr, sizeof(*tcp_hdr),
			      skb_transport_header(skb)) == 0;
}

static __always_inline u8 skb_sk_state(struct sk_buff *skb)
{
	return BPF_CORE_READ(skb, sk, __sk_common.skc_state);
}

#endif /* __BPF_SKBUFF_H__ */

#ifndef __VMLINUX_NET_H__
#define __VMLINUX_NET_H__

#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>

#define IFNAMSIZ        16

#define ETH_P_IP        0x0800          /* Internet Protocol packet     */
#define ETH_P_IPV6      0x86DD
#define ETH_P_ARP       0x0806
#define AF_INET         2       /* Internet IP Protocol         */
#define AF_INET6        10
#define IPPROTO_TCP     6
#define TCP_CLOSE       7

#define IP_MF           0x2000          /* Flag: "More Fragments"       */
#define IP_OFFSET       0x1FFF          /* "Fragment Offset" part       */

// skb_mac_header - return the MAC header pointer from sk_buff
static inline unsigned char *skb_mac_header(struct sk_buff *skb)
{
    return BPF_CORE_READ(skb, head) + BPF_CORE_READ(skb, mac_header);
}

// skb_network_header - get the network header from sk_buff
static inline unsigned char *skb_network_header(struct sk_buff *skb)
{
    return BPF_CORE_READ(skb, head) + BPF_CORE_READ(skb, network_header);
}

// skb_transport_header - get the transport header from sk_buff
static inline unsigned char *skb_transport_header(struct sk_buff *skb)
{
    return BPF_CORE_READ(skb, head) + BPF_CORE_READ(skb, transport_header);
}
#endif

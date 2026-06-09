#ifndef __BPF_NETNAMESPACE_H__
#define __BPF_NETNAMESPACE_H__

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>

static __always_inline u64 skb_netns_cookie(struct sk_buff *skb)
{
	if (!bpf_core_field_exists(((struct net*)0)->net_cookie))
		return 0;

	struct net_device *dev = BPF_CORE_READ(skb, dev);
	if (dev) {
		return BPF_CORE_READ(dev, nd_net.net, net_cookie);
	}

	struct sock *sk = BPF_CORE_READ(skb, sk);
	if (sk) {
		return BPF_CORE_READ(sk, __sk_common.skc_net.net, net_cookie);
	}

	return 0;
}

static __always_inline u32 skb_netns_inum(struct sk_buff *skb)
{
	struct net_device *dev = BPF_CORE_READ(skb, dev);
	if (dev) {
		return BPF_CORE_READ(dev, nd_net.net, ns.inum);
	}

	struct sock *sk = BPF_CORE_READ(skb, sk);
	if (sk) {
		return BPF_CORE_READ(sk, __sk_common.skc_net.net, ns.inum);
	}

	return 0;
}

#endif

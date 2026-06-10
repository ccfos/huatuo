/* SPDX-License-Identifier: (LGPL-2.1 OR BSD-2-Clause) */
#ifndef __BPF_SKB_FILTER_H__
#define __BPF_SKB_FILTER_H__

#include <bpf/bpf_core_read.h>

#include "vmlinux_net.h"

/*
 * Device filter injected via RewriteConstants at load time.
 *
 * filter_ifindex_included == 0  : disabled — all devices pass (default)
 * filter_ifindex_included != 0  : compare skb->dev->ifindex against this value
 * filter_ifindex_excluded == 0  : whitelist — only matching ifindex passes
 * filter_ifindex_excluded == 1  : blacklist — matching ifindex is dropped
 *
 * Each .c file that includes this header gets its own private copies of
 * the variables (static linkage), rewritten independently per ELF load.
 */
static volatile const __u32 filter_ifindex_included  = 0;
static volatile const __u32 filter_ifindex_excluded = 0;

/*
 * skb_filter_pass_dev - device filter check for a kfree_skb-style program.
 *
 * Returns true if the SKB should be processed, false if it should be skipped.
 * When filter_ifindex_included == 0 the check is a no-op (returns true immediately).
 */
static __always_inline bool skb_filter_pass_dev(struct sk_buff *skb)
{
	// Early return when filter is disabled.
	if (filter_ifindex_included == 0)
		return true;

	struct net_device *dev = BPF_CORE_READ(skb, dev);
	__u32 idx = dev ? BPF_CORE_READ(dev, ifindex) : 0;
	bool match = (idx == filter_ifindex_included);

	return filter_ifindex_excluded ? !match : match;
}

#endif /* __BPF_SKB_FILTER_H__ */

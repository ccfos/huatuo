/* SPDX-License-Identifier: (LGPL-2.1 OR BSD-2-Clause) */
#ifndef __BPF_SKB_FILTER_H__
#define __BPF_SKB_FILTER_H__

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

#include "vmlinux_net.h"

/*
 * Device filter injected via RewriteConstants + map population at load time.
 *
 * filter_dev_mode == 0 : disabled — all devices pass (default)
 * filter_dev_mode == 1 : whitelist — only ifindexes present in
 *                        skb_filter_dev_map pass
 * filter_dev_mode == 2 : blacklist — ifindexes present in
 *                        skb_filter_dev_map are dropped, others pass
 *
 * --device and --device-excluded are mutually exclusive, so a single map
 * suffices: the mode constant decides how a hit is interpreted.
 */
#define SKB_FILTER_DEV_MAP_MAX_ENTRIES 64

static volatile const __u32 filter_dev_mode = 0;

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, SKB_FILTER_DEV_MAP_MAX_ENTRIES);
	__type(key, __u32);
	__type(value, __u8);
} skb_filter_dev_map SEC(".maps");

/*
 * skb_filter_pass_dev - device filter check for a kfree_skb-style program.
 *
 * Returns true if the SKB should be processed, false if it should be skipped.
 * When filter_dev_mode == 0 the check is a no-op (returns true immediately).
 *
 * SKBs with no associated net_device (skb->dev == NULL) are classified as
 * ifindex 0. Kernel ifindex allocation starts at 1 and reserves 0 for
 * "unspecified", so a real device can never collide with idx 0, and userspace
 * --device / --device-excluded can only resolve named interfaces (ifindex >=
 * 1). NULL dev therefore always misses the map, which yields the strict
 * semantics of each flag:
 *   --device (whitelist): NULL dev is dropped (not in the listed set).
 *   --device-excluded (blacklist): NULL dev passes (not in the excluded set).
 */
static __always_inline bool skb_filter_pass_dev(struct sk_buff *skb)
{
	if (filter_dev_mode == 0)
		return true;

	struct net_device *dev = BPF_CORE_READ(skb, dev);
	__u32 idx = dev ? BPF_CORE_READ(dev, ifindex) : 0;
	bool hit = bpf_map_lookup_elem(&skb_filter_dev_map, &idx) != NULL;

	/* mode 1: whitelist (hit passes); mode 2: blacklist (hit dropped) */
	return filter_dev_mode == 1 ? hit : !hit;
}

#endif /* __BPF_SKB_FILTER_H__ */

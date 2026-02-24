#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"

char __license[] SEC("license") = "Dual MIT/GPL";

/*
 * Hash map for tracking network device packet drop statistics
 * Key:   Network interface index (ifindex)
 * Value: Received drop count (rx_dropped)
 */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 64);
	__type(key, u32);
	__type(value, u64);
} rx_sw_dropped_stats SEC(".maps");

/*
 * kprobe/carrier_down_count_show - Track packet drop statistics when
 *                                  network device carrier state changes
 * @dev: Pointer to device structure
 */
SEC("kprobe/carrier_down_count_show")
int BPF_KPROBE(carrier_down_count_show, struct device *dev)
{
	struct net_device *netdev = container_of(dev, struct net_device, dev);
	u32 key			  = BPF_CORE_READ(netdev, ifindex);
	u64 value		  = 0;

	if (bpf_core_field_exists(netdev->rx_dropped))
		value = BPF_CORE_READ(netdev, rx_dropped.counter);

	bpf_map_update_elem(&rx_sw_dropped_stats, &key, &value, COMPAT_BPF_ANY);
	return 0;
}

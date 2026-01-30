#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"

enum lat_zone {
	LAT_ZONE0 = 0, // 0 ~ 10us
	LAT_ZONE1,     // 10us ~ 100us
	LAT_ZONE2,     // 100us ~ 1ms
	LAT_ZONE3,     // 1ms ~ inf
	LAT_ZONE_MAX,
};

struct softirq_lat {
	u64 enable;
	u64 timestamp;
	u64 total_latency[LAT_ZONE_MAX];
};

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(struct softirq_lat));
	__uint(max_entries, NR_SOFTIRQS_MAX);
} softirq_percpu_lats SEC(".maps");

SEC("tracepoint/irq/softirq_raise")
int probe_softirq_raise(struct trace_event_raw_softirq *ctx)
{
	struct softirq_lat *lat;
	u32 vec = ctx->vec;

	if (vec >= NR_SOFTIRQS)
		return 0;

	lat = bpf_map_lookup_elem(&softirq_percpu_lats, &vec);
	if (!lat) {
		struct softirq_lat lat_init = {
			.timestamp = bpf_ktime_get_ns(),
			.enable = 1,
		};
		bpf_map_update_elem(&softirq_percpu_lats, &vec, &lat_init, COMPAT_BPF_ANY);

		return 0;
	}

	if (!lat->enable) {
		lat->timestamp = bpf_ktime_get_ns();
		lat->enable = 1;
	}

	return 0;
}

SEC("tracepoint/irq/softirq_entry")
int probe_softirq_entry(struct trace_event_raw_softirq *ctx)
{
	struct softirq_lat *lat;
	u32 vec = ctx->vec;

	if (vec >= NR_SOFTIRQS)
		return 0;

	lat = bpf_map_lookup_elem(&softirq_percpu_lats, &vec);
	if (!lat)
		return 0;

	if (!lat->enable)
		return 0;

	u64 latency = bpf_ktime_get_ns() - lat->timestamp;

	if (latency < 10 * NSEC_PER_USEC) {
		__sync_fetch_and_add(&lat->total_latency[LAT_ZONE0], 1);
	} else if (latency < 100 * NSEC_PER_USEC) {
		__sync_fetch_and_add(&lat->total_latency[LAT_ZONE1], 1);
	} else if (latency < 1 * NSEC_PER_MSEC) {
		__sync_fetch_and_add(&lat->total_latency[LAT_ZONE2], 1);
	} else {
		__sync_fetch_and_add(&lat->total_latency[LAT_ZONE3], 1);
	}

	lat->enable = 0;

	return 0;
}

char __license[] SEC("license") = "Dual MIT/GPL";

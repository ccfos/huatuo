#include "vmlinux.h"

#include "bpf_common.h"
#include "bpf_ratelimit.h"
#include "bpf_tracepoint.h"
#include "vmlinux_net.h"

char __license[] SEC("license") = "Dual MIT/GPL";

struct txqueue_timeout {
	unsigned int queue_index;
	char name[IFNAMSIZ];
	char driver[IFNAMSIZ];
};

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} perf_events SEC(".maps");

SEC("tracepoint/net/net_dev_xmit_timeout")
int bpf_rxqueue_timeout(struct trace_event_raw_net_dev_xmit_timeout *ctx)
{
	struct txqueue_timeout data = {
		.queue_index = ctx->queue_index,
	};

	bpf_probe_read_str(&data.name, sizeof(data.name),
			   __data_loc_address((char *)ctx, ctx->__data_loc_name));
	bpf_probe_read_str(&data.driver, sizeof(data.driver),
			   __data_loc_address((char *)ctx, ctx->__data_loc_driver));

	bpf_perf_event_output(ctx, &perf_events, COMPAT_BPF_F_CURRENT_CPU,
			      &data, sizeof(data));
	return 0;
}

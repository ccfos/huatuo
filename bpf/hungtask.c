#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_compat_7_0.h"
#include "bpf_ratelimit.h"

char __license[] SEC("license") = "Dual MIT/GPL";

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} hungtask_perf_events SEC(".maps");

struct hungtask_info {
	int32_t pid;
	char comm[COMPAT_TASK_COMM_LEN];
};

SEC("tracepoint/sched/sched_process_hang")
int tracepoint_sched_process_hang(struct trace_event_raw_sched_process_hang *ctx)
{
	struct hungtask_info info = {};

	info.pid = ctx->pid;

	/*
	 * trace_event_raw_sched_process_hang::comm changed across kernels:
	 *   pre-7.0: fixed-size __array(char, comm, TASK_COMM_LEN)
	 *   7.0+:    __string(comm, ...) -> u32 __data_loc_comm offset/length
	 */
	if (bpf_core_field_exists(ctx->comm)) {
		BPF_CORE_READ_STR_INTO(&info.comm, ctx, comm);
	} else {
		struct trace_event_raw_sched_process_hang___7_0 *ctx7 =
			(struct trace_event_raw_sched_process_hang___7_0 *)ctx;
		u32 dl = BPF_CORE_READ(ctx7, __data_loc_comm);

		bpf_probe_read_str(info.comm, sizeof(info.comm),
				   (void *)ctx + (dl & 0xffff));
	}

	bpf_perf_event_output(ctx, &hungtask_perf_events,
			      COMPAT_BPF_F_CURRENT_CPU, &info, sizeof(info));
	return 0;
}

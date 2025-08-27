#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
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
	BPF_CORE_READ_STR_INTO(&info.comm, ctx, comm);
	bpf_perf_event_output(ctx, &hungtask_perf_events,
			      COMPAT_BPF_F_CURRENT_CPU, &info, sizeof(info));
	return 0;
}

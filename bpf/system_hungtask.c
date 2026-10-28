#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "abi/hungtask_types.h"

char __license[] SEC("license") = "Dual MIT/GPL";

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} hungtask_perf_events SEC(".maps");

SEC("raw_tracepoint/sched_process_hang")
int raw_sched_process_hang(struct bpf_raw_tracepoint_args *ctx)
{
	/* The current task is the detector, not the blocked task. */
	struct task_struct *task = (void *)ctx->args[0];
	struct hungtask_event info = {};
	u32 cpu = bpf_core_enum_value(enum cgroup_subsys_id, cpu_cgrp_id);

	info.tid = BPF_CORE_READ(task, pid);
	BPF_CORE_READ_STR_INTO(&info.comm, task, comm);
	info.cpu_css_addr = (u64)BPF_CORE_READ(task, cgroups, subsys[cpu]);

	bpf_perf_event_output(ctx, &hungtask_perf_events,
			      COMPAT_BPF_F_CURRENT_CPU, &info, sizeof(info));
	return 0;
}

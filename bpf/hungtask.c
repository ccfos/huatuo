#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_ratelimit.h"
#include "bpf_tracepoint.h"
#include "abi/hungtask_types.h"

char __license[] SEC("license") = "Dual MIT/GPL";

const volatile u32 unified_cgroups = 0;

struct kernfs_node___id64 {
	u64 id;
} __attribute__((preserve_access_index));

struct kernfs_node___legacy {
	union {
		u64 id;
	} id;
} __attribute__((preserve_access_index));

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
	struct css_set *css;
	struct cgroup *cg;
	struct kernfs_node *kn;

	info.tid = BPF_CORE_READ(task, pid);
	BPF_CORE_READ_STR_INTO(&info.comm, task, comm);
	css = BPF_CORE_READ(task, cgroups);
	if (unified_cgroups) {
		cg = BPF_CORE_READ(css, dfl_cgrp);
	} else {
		u32 cpu = bpf_core_enum_value(enum cgroup_subsys_id, cpu_cgrp_id);
		cg = BPF_CORE_READ(css, subsys[cpu], cgroup);
	}
	kn = BPF_CORE_READ(cg, kn);
#pragma unroll
	for (int i = 0; i < 16; i++) {
		struct kernfs_node *parent = NULL;
		u64 id = 0;
		long err;

		if (!kn || BPF_CORE_READ_INTO(&parent, kn, parent) || !parent)
			break;
		/* Preserve generation bits, so recycled inode numbers cannot match. */
		if (bpf_core_field_exists(((struct kernfs_node___legacy *)kn)->id.id))
			err = BPF_CORE_READ_INTO(&id, (struct kernfs_node___legacy *)kn, id.id);
		else
			err = BPF_CORE_READ_INTO(&id, (struct kernfs_node___id64 *)kn, id);
		if (err || !id)
			break;
		info.cgroup_ids[i] = id;
		info.cgroup_count++;
		kn = parent;
	}

	bpf_perf_event_output(ctx, &hungtask_perf_events,
			      COMPAT_BPF_F_CURRENT_CPU, &info, sizeof(info));
	return 0;
}

SEC("tracepoint/sched/sched_process_hang")
int tracepoint_sched_process_hang(struct trace_event_raw_sched_process_hang *ctx)
{
	struct hungtask_event info = {};

	/* sched_process_hang::pid identifies the hung task's TID. */
	info.tid = ctx->pid;

	/*
	 * trace_event_raw_sched_process_hang::comm changed across kernels:
	 *   pre-7.0: fixed-size __array(char, comm, TASK_COMM_LEN)
	 *   7.0+:    __string(comm, ...) -> u32 __data_loc_comm offset/length
	 */
	if (bpf_core_field_exists(ctx->comm)) {
		BPF_CORE_READ_STR_INTO(&info.comm, ctx, comm);
	} else {
		struct trace_event_raw_sched_process_hang___7_0_compat *ctx7 =
			(struct trace_event_raw_sched_process_hang___7_0_compat *)ctx;
		u32 dl = BPF_CORE_READ(ctx7, __data_loc_comm);

		bpf_probe_read_str(info.comm, sizeof(info.comm),
				   (void *)ctx + (dl & 0xffff));
	}

	bpf_perf_event_output(ctx, &hungtask_perf_events,
			      COMPAT_BPF_F_CURRENT_CPU, &info, sizeof(info));
	return 0;
}

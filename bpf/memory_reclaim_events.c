#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_func_trace.h"
#include "bpf_ratelimit.h"
#include "abi/memory_reclaim_types.h"

char __license[] SEC("license") = "Dual MIT/GPL";

volatile const unsigned long deltath = 0;

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} reclaim_perf_events SEC(".maps");

SEC("kprobe/try_to_free_pages")
int kprobe_try_to_free_pages(struct pt_regs *ctx)
{
	func_trace_begain(bpf_get_current_pid_tgid());
	return 0;
}

SEC("kretprobe/try_to_free_pages")
int kretprobe_try_to_free_pages(struct pt_regs *ctx)
{
	struct trace_entry_ctx *entry;
	struct task_struct *task;

	entry = func_trace_end(bpf_get_current_pid_tgid());
	if (!entry)
		return 0;

	if (entry->delta_ns > deltath) {
		task = (struct task_struct *)bpf_get_current_task();

		struct memory_reclaim_event data = {
			.pid	    = entry->id,
			.css	    = (u64)BPF_CORE_READ(task, cgroups,
							 subsys[cpu_cgrp_id]),
			.delta_time = entry->delta_ns,
		};

		bpf_get_current_comm(data.comm, sizeof(data.comm));

		bpf_perf_event_output(ctx, &reclaim_perf_events,
				      COMPAT_BPF_F_CURRENT_CPU, &data,
				      sizeof(struct memory_reclaim_event));
	}

	func_trace_destroy(entry->id);
	return 0;
}

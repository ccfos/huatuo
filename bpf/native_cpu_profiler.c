#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_profiler.h"
#include "bpf_dbg.h"
#include "bpf_map.h"

char __license[] SEC("license") = "Dual MIT/GPL";

/*
 * CPU filtering (--cpuid) is handled entirely at the PMU layer.
 * No BPF-side cpuid check is needed.
 */

BPF_DBG_MAP(native_cpu_dbg);

struct cpu_event_t {
	struct profiler_event_base_t base;
	__u64 timestamp;
	__u32 cpu;
	__u32 pad0;
};

DEFINE_PROFILER_MAPS(struct cpu_event_t);

SEC("perf_event/software/cpu_clock")
int perf_event_sw_cpu_clock(struct pt_regs *ctx)
{
	u64 *transfer_count_ptr;
	u64 *sample_count_ptrs[2];
	void *select_profiler_stack_map;
	void *select_profiler_output;
	u64 *select_profiler_sample_count_ptr;

	if (!profiler_init_state(&profiler_state_map, &transfer_count_ptr, sample_count_ptrs))
		return 0;

	struct task_struct *curr = (struct task_struct *)bpf_get_current_task();
	u64 cpu_css = current_task_cpu_css_addr();
	u64 sched_class = (u64)BPF_CORE_READ(curr, sched_class);

	u64 pid_tgid = bpf_get_current_pid_tgid();

	if (!profiler_should_trace_cpu(pid_tgid, cpu_css, sched_class)) {
		bpf_dbg_msg(ctx, native_cpu_dbg, "filter missed");
		return 0;
	}

	u32 idx = 0;
	struct cpu_event_t *event = bpf_map_lookup_elem(&event_buf, &idx);
	if (!event)
		return 0;

	SELECT_PROFILER_AB();

	event->base.pid_tgid = pid_tgid;
	bpf_get_current_comm(&event->base.comm, sizeof(event->base.comm));

	event->cpu = bpf_get_smp_processor_id();
	event->timestamp = bpf_ktime_get_ns();

	event->base.userstack = bpf_get_stackid(ctx, select_profiler_stack_map, USER_STACKID_FLAGS);
	event->base.kernstack = bpf_get_stackid(ctx, select_profiler_stack_map, KERN_STACKID_FLAGS);
	event->base.value = 1;

	if (event->base.userstack < 0 && event->base.kernstack < 0) {
		bpf_dbg_msg(ctx, native_cpu_dbg, "stack missed");
		return 0;
	}

	/*
	 * Global ARRAY + atomic add is intentional; do NOT switch to PERCPU.
	 * See comment in original code for details.
	 */
	__sync_fetch_and_add(select_profiler_sample_count_ptr, 1);

	bpf_perf_event_output(ctx, select_profiler_output, COMPAT_BPF_F_CURRENT_CPU,
	                      event, sizeof(struct cpu_event_t));

	return 0;
}

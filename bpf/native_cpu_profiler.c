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
	u32 tgid = pid_tgid >> 32;
	u32 pid = pid_tgid & 0xffffffffUL;

	if (!profiler_should_trace(pid_tgid, cpu_css) ||
	    (profiler_idle_class_addr != 0 && sched_class == profiler_idle_class_addr) ||
	    (tgid == 0 && pid == 0)) {
		bpf_dbg_msg(ctx, native_cpu_dbg, "filter missed");
		return 0;
	}

	u32 idx = 0;
	struct cpu_event_t *event = bpf_map_lookup_elem(&event_buf, &idx);
	if (!event)
		return 0;

	SELECT_PROFILER_AB();

	event->cpu = bpf_get_smp_processor_id();
	event->timestamp = bpf_ktime_get_ns();
	event->base.value = 1;

	if (profiler_fill_event_base(&event->base, pid_tgid, ctx, select_profiler_stack_map) < 0) {
		bpf_dbg_msg(ctx, native_cpu_dbg, "stack missed");
		return 0;
	}

	profiler_emit_event(ctx, select_profiler_output,
	                    select_profiler_sample_count_ptr, event, sizeof(*event));

	return 0;
}

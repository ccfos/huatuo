#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_core_read.h>
#include "bpf_profiler.h"

char __license[] SEC("license") = "GPL";

DEFINE_PROFILER_MAPS(struct profiler_event_base_t);

SEC("kprobe/page_add_new_anon_rmap")
int BPF_KPROBE(trace_page_alloc, struct page *page,
               struct vm_area_struct *vma, unsigned long address, bool compound)
{
	u64 *transfer_count_ptr;
	u64 *sample_count_ptrs[2];
	void *select_profiler_stack_map;
	void *select_profiler_output;
	u64 *select_profiler_sample_count_ptr;

	if (!profiler_init_state(&profiler_state_map, &transfer_count_ptr, sample_count_ptrs))
		return 0;

	u64 pid_tgid = bpf_get_current_pid_tgid();
	if (!profiler_should_trace(pid_tgid))
		return 0;

	if (!profiler_should_sample())
		return 0;

	u32 idx = 0;
	struct profiler_event_base_t *event = bpf_map_lookup_elem(&event_buf, &idx);
	if (!event)
		return 0;

	SELECT_PROFILER_AB();

	__builtin_memset(event, 0, sizeof(*event));

	if (profiler_fill_event_base(event, ctx, select_profiler_stack_map) < 0)
		return 0;

	event->value = 1;

	__sync_fetch_and_add(select_profiler_sample_count_ptr, 1);
	bpf_perf_event_output(ctx, select_profiler_output, COMPAT_BPF_F_CURRENT_CPU,
	                      event, sizeof(*event));

	return 0;
}

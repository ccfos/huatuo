#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_core_read.h>
#include "bpf_profiler.h"

char __license[] SEC("license") = "GPL";

DEFINE_PROFILER_PAGE_TRACKING_MAP();
DEFINE_PROFILER_MAPS(struct profiler_event_base_t);

SEC("kprobe/page_add_new_anon_rmap")
int BPF_KPROBE(trace_page_alloc, struct page *page,
               struct vm_area_struct *vma, unsigned long address, bool compound)
{
	u64 *transfer_count_ptr;
	u64 *sample_count_ptrs[2];
	void *select_profiler_stack_map __attribute__((unused));
	void *select_profiler_output;
	u64 *select_profiler_sample_count_ptr;

	if (!profiler_init_state(&profiler_state_map, &transfer_count_ptr, sample_count_ptrs))
		return 0;

	u64 pid_tgid = bpf_get_current_pid_tgid();
	u64 mem_css = 0;
	if (profiler_filter_css != 0)
		mem_css = current_task_memory_css_addr();
	if (!profiler_should_trace(pid_tgid, mem_css))
		return 0;

	if (!profiler_should_sample())
		return 0;

	SELECT_PROFILER_AB();

	struct profiler_event_base_t *event = profiler_prepare_event_base(
		&event_buf, pid_tgid, ctx, select_profiler_stack_map);
	if (!event)
		return 0;

	event->value = 1;

	u64 page_addr = (u64)page;
	/*
	 * The hash map stores a value copy, so later event_buf reuse does not
	 * overwrite the allocation stack saved for this page address.
	 */
	bpf_map_update_elem(&page_to_stackid, &page_addr, event, COMPAT_BPF_ANY);

	profiler_emit_event(ctx, select_profiler_output,
	                    select_profiler_sample_count_ptr, event, sizeof(*event));

	return 0;
}

SEC("kprobe/page_remove_rmap")
int BPF_KPROBE(trace_page_free, struct page *page, bool compound)
{
	u64 *transfer_count_ptr;
	u64 *sample_count_ptrs[2];
	void *select_profiler_stack_map __attribute__((unused));
	void *select_profiler_output;
	u64 *select_profiler_sample_count_ptr;

	if (!profiler_init_state(&profiler_state_map, &transfer_count_ptr, sample_count_ptrs))
		return 0;

	u64 pid_tgid = bpf_get_current_pid_tgid();
	u64 mem_css = 0;
	if (profiler_filter_css != 0)
		mem_css = current_task_memory_css_addr();
	if (!profiler_should_trace(pid_tgid, mem_css))
		return 0;

	u64 page_addr = (u64)page;
	struct profiler_event_base_t *stack_info =
		bpf_map_lookup_elem(&page_to_stackid, &page_addr);
	if (!stack_info)
		return 0;

	u32 idx = 0;
	struct profiler_event_base_t *event = bpf_map_lookup_elem(&event_buf, &idx);
	if (!event)
		return 0;

	__builtin_memset(event, 0, sizeof(*event));

	profiler_copy_event_base(event, stack_info);
	event->value = -1;

	bpf_map_delete_elem(&page_to_stackid, &page_addr);

	SELECT_PROFILER_AB();

	profiler_emit_event(ctx, select_profiler_output,
	                    select_profiler_sample_count_ptr, event, sizeof(*event));

	return 0;
}

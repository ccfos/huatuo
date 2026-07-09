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
	GET_PROFILER_STATE_POINTERS(transfer_count_ptr, sample_count_ptrs);

	if (!transfer_count_ptr || !sample_count_ptrs[0] || !sample_count_ptrs[1])
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

	void *stack_map;
	void *profiler_output;
	u64 *sample_count_ptr;

	SELECT_PROFILER_AB(transfer_count_ptr, sample_count_ptrs,
	                   sample_count_ptr, stack_map, profiler_output);

	__builtin_memset(event, 0, sizeof(*event));

	if (profiler_fill_event_base(event, ctx, stack_map) < 0)
		return 0;

	event->value = 1;

	__sync_fetch_and_add(sample_count_ptr, 1);
	bpf_perf_event_output(ctx, profiler_output, COMPAT_BPF_F_CURRENT_CPU,
	                      event, sizeof(*event));

	return 0;
}
#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_core_read.h>
#include "bpf_profiler.h"

char __license[] SEC("license") = "GPL";

DEFINE_PROFILER_MAPS(struct profiler_event_base);

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 4096);
	__type(key, u64);
	__type(value, struct profiler_event_base);
} mmap_start_map SEC(".maps");

/* Reuse final-drain attempted/delivered accounting for lost mmap samples. */
static __always_inline void record_mmap_drop(void)
{
	u32 index = PROFILER_STATE_TRANSFER_CNT_IDX;
	u64 *transfer = bpf_map_lookup_elem(&profiler_state_map, &index);

	if (!transfer)
		return;
	index = (*transfer & 1) ? PROFILER_STATE_SAMPLE_CNT_B_IDX :
		PROFILER_STATE_SAMPLE_CNT_A_IDX;
	u64 *attempts = bpf_map_lookup_elem(&profiler_state_map, &index);

	if (attempts)
		__sync_fetch_and_add(attempts, 1);
}

SEC("kprobe/do_mmap")
int BPF_KPROBE(trace_mmap, struct file *file, unsigned long addr,
               unsigned long len)
{
	u64 pid_tgid = bpf_get_current_pid_tgid();
	u64 mem_css = 0;

	/* A later entry detects a missed return, even if this call is filtered. */
	if (bpf_map_lookup_elem(&mmap_start_map, &pid_tgid)) {
		record_mmap_drop();
		bpf_map_delete_elem(&mmap_start_map, &pid_tgid);
	}
	if (file)
		return 0;
	if (profiler_filter_css_addr != 0)
		mem_css = current_task_memory_css_addr();
	if (!profiler_should_trace(pid_tgid, mem_css))
		return 0;

	/* Pending calls can outlive a swap, so both readers use stack_map_a. */
	struct profiler_event_base *event = profiler_prepare_event_base(
		&event_buf, pid_tgid, ctx, &stack_map_a);
	if (!event)
		return 0;
	event->value = (s64)len;
	if (bpf_map_update_elem(&mmap_start_map, &pid_tgid, event, COMPAT_BPF_ANY))
		record_mmap_drop();
	return 0;
}

SEC("kretprobe/do_mmap")
int BPF_KRETPROBE(trace_mmap_return, unsigned long ret)
{
	u64 pid_tgid = bpf_get_current_pid_tgid();
	struct profiler_event_base *pending = bpf_map_lookup_elem(&mmap_start_map, &pid_tgid);
	struct profiler_event_base saved;

	if (!pending)
		return 0;
	saved = *pending;
	bpf_map_delete_elem(&mmap_start_map, &pid_tgid);
	/* do_mmap returns an address or an encoded errno, not a signed count. */
	if (ret >= (unsigned long)-4095)
		return 0;

	u64 *transfer_count_ptr;
	u64 *sample_count_ptrs[2];
	void *select_profiler_output;
	u64 *select_profiler_sample_count_ptr;

	if (!profiler_init_state(&profiler_state_map, &transfer_count_ptr, sample_count_ptrs))
		return 0;

	/* Freeze only completed calls; their entry stacks remain in the shared map. */
	if (((*transfer_count_ptr) & 1ULL) == 0) {
		select_profiler_output = (void *)&profiler_output_a;
		select_profiler_sample_count_ptr = sample_count_ptrs[0];
	} else {
		select_profiler_output = (void *)&profiler_output_b;
		select_profiler_sample_count_ptr = sample_count_ptrs[1];
	}
	u32 zero = 0;
	struct profiler_event_base *event = bpf_map_lookup_elem(&event_buf, &zero);
	if (!event)
		return 0;
	*event = saved;
	profiler_emit_event(ctx, select_profiler_output,
	                    select_profiler_sample_count_ptr, event, sizeof(*event));
	return 0;
}

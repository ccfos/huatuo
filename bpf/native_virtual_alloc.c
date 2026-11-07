#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_core_read.h>
#include "bpf_profiler.h"

char __license[] SEC("license") = "GPL";

DEFINE_PROFILER_MAPS(struct profiler_event_base);

struct mmap_event {
	struct profiler_event_base base;
	u8 output_index;
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 10240);
	__type(key, u64);
	__type(value, struct mmap_event);
} mmap_lengths SEC(".maps");

SEC("kprobe/do_mmap")
int BPF_KPROBE(trace_mmap, struct file *file, unsigned long addr,
               unsigned long len)
{
	u64 pid_tgid = bpf_get_current_pid_tgid();
	u64 mem_css = 0;
	u64 *transfer_count_ptr;
	u64 *sample_count_ptrs[2];
	void *select_profiler_stack_map;
	struct mmap_event pending = {};

	if (file) {
		/* Drop a stale entry before a call that cannot produce an event. */
		bpf_map_delete_elem(&mmap_lengths, &pid_tgid);
		return 0;
	}
	if (profiler_filter_css != 0)
		mem_css = current_task_memory_css_addr();
	if (!profiler_should_trace(pid_tgid, mem_css)) {
		bpf_map_delete_elem(&mmap_lengths, &pid_tgid);
		return 0;
	}

	if (!profiler_init_state(&profiler_state_map, &transfer_count_ptr,
				 sample_count_ptrs))
		return 0;

	if (((*(transfer_count_ptr)) & 0x1ULL) == 0) {
		select_profiler_stack_map = (void *)&stack_map_a;
		pending.output_index = 0;
	} else {
		select_profiler_stack_map = (void *)&stack_map_b;
		pending.output_index = 1;
	}

	struct profiler_event_base *event = profiler_prepare_event_base(
		&event_buf, pid_tgid, ctx, select_profiler_stack_map);
	if (!event)
		return 0;

	event->value = (s64)len;
	__builtin_memcpy(&pending.base, event, sizeof(pending.base));
	bpf_map_update_elem(&mmap_lengths, &pid_tgid, &pending,
			    COMPAT_BPF_ANY);
	return 0;
}

SEC("kretprobe/do_mmap")
int BPF_KRETPROBE(trace_mmap_return, unsigned long ret)
{
	u64 pid_tgid = bpf_get_current_pid_tgid();
	struct mmap_event *pending = bpf_map_lookup_elem(&mmap_lengths, &pid_tgid);
	struct mmap_event event;

	if (!pending)
		return 0;

	__builtin_memcpy(&event, pending, sizeof(event));
	bpf_map_delete_elem(&mmap_lengths, &pid_tgid);

	/* do_mmap returns an address or an encoded errno, not a signed count. */
	if (ret >= (unsigned long)-4095)
		return 0;

	u64 *transfer_count_ptr;
	u64 *sample_count_ptrs[2];
	void *select_profiler_output;
	u64 *select_profiler_sample_count_ptr;

	if (!profiler_init_state(&profiler_state_map, &transfer_count_ptr, sample_count_ptrs))
		return 0;

	if (event.output_index == 0) {
		select_profiler_output = (void *)&profiler_output_a;
		select_profiler_sample_count_ptr = sample_count_ptrs[0];
	} else {
		select_profiler_output = (void *)&profiler_output_b;
		select_profiler_sample_count_ptr = sample_count_ptrs[1];
	}

	u32 idx = 0;
	struct profiler_event_base *output =
		bpf_map_lookup_elem(&event_buf, &idx);
	if (!output)
		return 0;

	__builtin_memcpy(output, &event.base, sizeof(*output));

	profiler_emit_event(ctx, select_profiler_output,
			    select_profiler_sample_count_ptr, output,
			    sizeof(*output));

	return 0;
}

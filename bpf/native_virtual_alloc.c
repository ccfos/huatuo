#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_core_read.h>
#include "bpf_profiler.h"

char __license[] SEC("license") = "GPL";

DEFINE_PROFILER_MAPS(struct profiler_event_base);

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 10240);
	__type(key, u64);
	__type(value, u64);
} mmap_lengths SEC(".maps");

SEC("kprobe/do_mmap")
int BPF_KPROBE(trace_mmap, struct file *file, unsigned long addr,
               unsigned long len)
{
	u64 pid_tgid = bpf_get_current_pid_tgid();
	u64 mem_css = 0;

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

	bpf_map_update_elem(&mmap_lengths, &pid_tgid, &len, COMPAT_BPF_ANY);
	return 0;
}

SEC("kretprobe/do_mmap")
int BPF_KRETPROBE(trace_mmap_return, unsigned long ret)
{
	u64 pid_tgid = bpf_get_current_pid_tgid();
	u64 *length = bpf_map_lookup_elem(&mmap_lengths, &pid_tgid);
	u64 len;

	if (!length)
		return 0;
	len = *length;
	bpf_map_delete_elem(&mmap_lengths, &pid_tgid);

	/* do_mmap returns an address or an encoded errno, not a signed count. */
	if (ret >= (unsigned long)-4095)
		return 0;

	u64 *transfer_count_ptr;
	u64 *sample_count_ptrs[2];
	void *select_profiler_stack_map;
	void *select_profiler_output;
	u64 *select_profiler_sample_count_ptr;

	if (!profiler_init_state(&profiler_state_map, &transfer_count_ptr, sample_count_ptrs))
		return 0;

	/* Capture and publish together so stack IDs belong to the output buffer. */
	SELECT_PROFILER_AB();

	struct profiler_event_base *event = profiler_prepare_event_base(
		&event_buf, pid_tgid, ctx, select_profiler_stack_map);
	if (!event)
		return 0;

	event->value = (s64)len;

	profiler_emit_event(ctx, select_profiler_output,
	                    select_profiler_sample_count_ptr, event, sizeof(*event));

	return 0;
}

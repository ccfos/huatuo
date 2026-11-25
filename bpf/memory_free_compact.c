#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_cgroup.h"
#include "bpf_func_trace.h"
#include "abi/mm_free_compact_types.h"

/* Key zero is reserved by userspace before attachment for host-wide totals. */
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_HASH);
	__uint(map_flags, COMPAT_BPF_F_NO_PREALLOC);
	__type(key, u64);
	__type(value, struct mm_free_compact_entry);
	__uint(max_entries, 10241);
} mm_free_compact_map SEC(".maps");

char __license[] SEC("license") = "Dual MIT/GPL";

static __always_inline void
add_stall(struct mm_free_compact_entry *valp, u64 duration_ns, bool free_pages)
{
	if (!valp)
		return;
	/* Non-sleepable tracing writers are CPU-pinned and recursion-protected. */
	if (free_pages)
		valp->allocstall_stat += duration_ns;
	else
		valp->compaction_stat += duration_ns;
}

static __always_inline void stall_begin(void)
{
	u64 key = bpf_get_current_pid_tgid();
	u64 css = current_task_memory_css_addr();
	func_trace_begin(key, css);
}

static __always_inline void func_trace_end_and_update_metric(bool free_pages)
{
	u64 key = bpf_get_current_pid_tgid();
	struct trace_entry_ctx *entry = func_trace_end(key);
	if (!entry)
		return;

	u64 duration_ns = entry->duration_ns;
	u64 css = entry->id;
	u64 host_key = 0;
	add_stall(bpf_map_lookup_elem(&mm_free_compact_map, &host_key),
		  duration_ns, free_pages);

	/* A task moved to another CSS during the operation has ambiguous
	 * attribution. Keep its host duration without recreating an old key. */
	if (css &&
	    css == current_task_memory_css_addr()) {
		struct mm_free_compact_entry *valp =
			bpf_map_lookup_elem(&mm_free_compact_map, &css);
		if (!valp) {
			struct mm_free_compact_entry zero = {};
			/* Do not overwrite another CPU's first increment. */
			bpf_map_update_elem(&mm_free_compact_map, &css,
					    &zero, COMPAT_BPF_NOEXIST);
			valp = bpf_map_lookup_elem(&mm_free_compact_map, &css);
		}
		add_stall(valp, duration_ns, free_pages);
	}
	func_trace_destroy(key);
}

SEC("tracepoint/vmscan/mm_vmscan_direct_reclaim_begin")
int tracepoint_try_to_free_pages_begin(struct pt_regs *ctx)
{
	stall_begin();
	return 0;
}

SEC("tracepoint/vmscan/mm_vmscan_direct_reclaim_end")
int tracepoint_try_to_free_pages_end(struct pt_regs *ctx)
{
	func_trace_end_and_update_metric(true);
	return 0;
}

SEC("kprobe/try_to_compact_pages")
int kprobe_try_to_compact_pages_host(struct pt_regs *ctx)
{
	stall_begin();
	return 0;
}

SEC("kretprobe/try_to_compact_pages")
int kretprobe_try_to_compact_pages_host(struct pt_regs *ctx)
{
	func_trace_end_and_update_metric(false);
	return 0;
}

SEC("kprobe/mem_cgroup_css_released")
int kprobe_mem_cgroup_css_released(struct pt_regs *ctx)
{
	u64 css = PT_REGS_PARM1(ctx);
	if (css)
		bpf_map_delete_elem(&mm_free_compact_map, &css);
	return 0;
}

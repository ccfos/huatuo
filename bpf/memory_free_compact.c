#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

#include "bpf_common.h"
#include "bpf_func_trace.h"

struct mm_free_compact_entry {
	/* host: compaction latency */
	unsigned long compaction_stat;
	/* host: page alloc latency in direct reclaim */
	unsigned long allocstall_stat;
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, int);
	__type(value, struct mm_free_compact_entry);
	__uint(max_entries, 10240);
} mm_free_compact_map SEC(".maps");

char __license[] SEC("license") = "Dual MIT/GPL";

static __always_inline void
update_metric_map(u64 free_duration_ns, u64 compact_duration_ns)
{
	struct mm_free_compact_entry *valp;
	int key = 0;

	valp = bpf_map_lookup_elem(&mm_free_compact_map, &key);
	if (!valp) {
		struct mm_free_compact_entry new_metrics = {
			.allocstall_stat = free_duration_ns,
			.compaction_stat = compact_duration_ns,
		};
		bpf_map_update_elem(&mm_free_compact_map, &key, &new_metrics,
				    COMPAT_BPF_ANY);
		return;
	}

	if (free_duration_ns)
		__sync_fetch_and_add(&valp->allocstall_stat, free_duration_ns);

	if (compact_duration_ns)
		__sync_fetch_and_add(&valp->compaction_stat,
				     compact_duration_ns);
}

static __always_inline void func_trace_end_and_update_metric(bool free_pages)
{
	struct trace_entry_ctx *entry;

	entry = func_trace_end(bpf_get_current_pid_tgid());
	if (!entry)
		return;

	if (free_pages)
		update_metric_map(entry->duration_ns, 0);
	else
		update_metric_map(0, entry->duration_ns);

	func_trace_destroy(entry->id);
}

SEC("tracepoint/vmscan/mm_vmscan_direct_reclaim_begin")
int tracepoint_try_to_free_pages_begin(struct pt_regs *ctx)
{
	func_trace_begin(bpf_get_current_pid_tgid());
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
	func_trace_begin(bpf_get_current_pid_tgid());
	return 0;
}

SEC("kretprobe/try_to_compact_pages")
int kretprobe_try_to_compact_pages_host(struct pt_regs *ctx)
{
	func_trace_end_and_update_metric(false);
	return 0;
}

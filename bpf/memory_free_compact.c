#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

#include "bpf_common.h"
#include "bpf_cgroup.h"

/* Rewritten to zero when container CO-RE fields or cleanup hooks are absent. */
const volatile u32 enable_container_stalls = 1;

struct mm_free_compact_entry {
	/* host: compaction latency */
	u64 compaction_stat;
	/* host: page alloc latency in direct reclaim */
	u64 allocstall_stat;
};

struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__type(key, int);
	__type(value, struct mm_free_compact_entry);
	__uint(max_entries, 1);
} mm_free_compact_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__type(key, u64);
	__type(value, struct mm_free_compact_entry);
	__uint(max_entries, 10240);
} mm_container_free_compact_map SEC(".maps");

struct stall_key {
	u64 pid_tgid;
	u64 free_pages;
};

struct stall_start {
	u64 start_ns;
	u64 memory_css;
	u64 memory_serial;
};

/* Separate operation keys prevent reclaim and compaction from overwriting
 * each other's timing state. LRU bounds abandoned entries after task exit. */
struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__type(key, struct stall_key);
	__type(value, struct stall_start);
	__uint(max_entries, 10240);
} mm_stall_start SEC(".maps");

char __license[] SEC("license") = "Dual MIT/GPL";

static __always_inline void
add_stall(struct mm_free_compact_entry *valp, u64 duration_ns, bool free_pages)
{
	if (!valp)
		return;
	if (free_pages)
		__sync_fetch_and_add(&valp->allocstall_stat, duration_ns);
	else
		__sync_fetch_and_add(&valp->compaction_stat, duration_ns);
}

static __always_inline void stall_begin(bool free_pages)
{
	struct stall_key key = {
		.pid_tgid = bpf_get_current_pid_tgid(),
		.free_pages = free_pages,
	};
	struct stall_start start = {
		.start_ns = bpf_ktime_get_ns(),
	};
	if (enable_container_stalls) {
		start.memory_css = current_task_memory_css_addr();
		struct cgroup_subsys_state *css = (void *)start.memory_css;
		if (css)
			start.memory_serial = BPF_CORE_READ(css, serial_nr);
	}
	bpf_map_update_elem(&mm_stall_start, &key, &start, COMPAT_BPF_ANY);
}

static __always_inline void stall_end(bool free_pages)
{
	struct stall_key key = {
		.pid_tgid = bpf_get_current_pid_tgid(),
		.free_pages = free_pages,
	};
	struct stall_start *start = bpf_map_lookup_elem(&mm_stall_start, &key);
	if (!start)
		return;

	u64 duration_ns = bpf_ktime_get_ns() - start->start_ns;
	u64 css = start->memory_css;
	u64 serial = start->memory_serial;
	int host_key = 0;
	add_stall(bpf_map_lookup_elem(&mm_free_compact_map, &host_key),
		  duration_ns, free_pages);

	/* An in-flight task may migrate out while its original cgroup is freed.
	 * Do not charge a different cgroup that reused the old CSS address. */
	if (enable_container_stalls && css &&
	    serial == BPF_CORE_READ((struct cgroup_subsys_state *)css, serial_nr)) {
		struct mm_free_compact_entry *valp =
			bpf_map_lookup_elem(&mm_container_free_compact_map, &css);
		if (!valp) {
			struct mm_free_compact_entry zero = {};
			/* Do not overwrite another CPU's first increment. */
			bpf_map_update_elem(&mm_container_free_compact_map, &css,
					    &zero, COMPAT_BPF_NOEXIST);
			valp = bpf_map_lookup_elem(&mm_container_free_compact_map, &css);
		}
		add_stall(valp, duration_ns, free_pages);
	}
	bpf_map_delete_elem(&mm_stall_start, &key);
}

SEC("tracepoint/vmscan/mm_vmscan_direct_reclaim_begin")
int tracepoint_try_to_free_pages_begin(struct pt_regs *ctx)
{
	stall_begin(true);
	return 0;
}

SEC("tracepoint/vmscan/mm_vmscan_direct_reclaim_end")
int tracepoint_try_to_free_pages_end(struct pt_regs *ctx)
{
	stall_end(true);
	return 0;
}

SEC("kprobe/try_to_compact_pages")
int kprobe_try_to_compact_pages_host(struct pt_regs *ctx)
{
	stall_begin(false);
	return 0;
}

SEC("kretprobe/try_to_compact_pages")
int kretprobe_try_to_compact_pages_host(struct pt_regs *ctx)
{
	stall_end(false);
	return 0;
}

/* Clear reused CSS addresses before a new cgroup can inherit old counters.
 * Ignore inherited subsystem states when the memory controller is disabled. */
SEC("raw_tracepoint/cgroup_mkdir")
int memory_stall_cgroup_mkdir(struct bpf_raw_tracepoint_args *ctx)
{
	struct cgroup *cgrp = (void *)ctx->args[0];
	u64 id = bpf_core_enum_value(enum cgroup_subsys_id, memory_cgrp_id);
	struct cgroup_subsys_state *css = BPF_CORE_READ(cgrp, subsys[id]);
	if (css && BPF_CORE_READ(css, cgroup) == cgrp) {
		u64 key = (u64)css;
		bpf_map_delete_elem(&mm_container_free_compact_map, &key);
	}
	return 0;
}

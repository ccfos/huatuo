#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

#include "bpf_common.h"
#include "bpf_cgroup.h"

/* Rewritten to zero when container CO-RE fields are unavailable. */
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
	u64 cgroup_id;
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

/* kernfs IDs include the generation, unlike a reusable CSS address. */
struct kernfs_node___id64 {
	u64 id;
} __attribute__((preserve_access_index));

struct kernfs_node___legacy {
	union {
		u64 id;
	} id;
} __attribute__((preserve_access_index));

static __always_inline u64 memory_cgroup_id(void)
{
	struct cgroup_subsys_state *css = (void *)current_task_memory_css_addr();
	struct kernfs_node *kn = BPF_CORE_READ(css, cgroup, kn);
	u64 id = 0;
	long err;

	if (!kn)
		return 0;
	if (bpf_core_field_exists(((struct kernfs_node___legacy *)kn)->id.id))
		err = BPF_CORE_READ_INTO(&id, (struct kernfs_node___legacy *)kn, id.id);
	else
		err = BPF_CORE_READ_INTO(&id, (struct kernfs_node___id64 *)kn, id);
	return err ? 0 : id;
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
	if (enable_container_stalls)
		start.cgroup_id = memory_cgroup_id();
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
	u64 cgroup_id = start->cgroup_id;
	int host_key = 0;
	add_stall(bpf_map_lookup_elem(&mm_free_compact_map, &host_key),
		  duration_ns, free_pages);

	/* Save attribution at begin: late completion must stay with the old
	 * cgroup even after migration, controller teardown or address reuse. */
	if (enable_container_stalls && cgroup_id) {
		struct mm_free_compact_entry *valp =
			bpf_map_lookup_elem(&mm_container_free_compact_map, &cgroup_id);
		if (!valp) {
			struct mm_free_compact_entry zero = {};
			/* Do not overwrite another CPU's first increment. */
			bpf_map_update_elem(&mm_container_free_compact_map, &cgroup_id,
					    &zero, COMPAT_BPF_NOEXIST);
			valp = bpf_map_lookup_elem(&mm_container_free_compact_map, &cgroup_id);
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

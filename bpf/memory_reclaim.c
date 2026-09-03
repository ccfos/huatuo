#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_cgroup.h"
#include "bpf_common.h"
#include "vmlinux_sched.h"

char __license[] SEC("license") = "Dual MIT/GPL";

struct mem_cgroup_metric {
	/* cgroup direct reclaim counter caused by try_charge */
	unsigned long directstall_count;
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, struct container_cgroup_key);
	__type(value, struct mem_cgroup_metric);
	__uint(max_entries, 10240);
} memory_cgroup_allocpages_stall SEC(".maps");

SEC("tracepoint/vmscan/mm_vmscan_memcg_reclaim_begin")
int tracepoint_vmscan_mm_vmscan_memcg_reclaim_begin(struct pt_regs *ctx)
{
	struct container_cgroup_key key;
	struct mem_cgroup_metric *valp;
	struct task_struct *task;

	task = (struct task_struct *)bpf_get_current_task();
	if (BPF_CORE_READ(task, flags) & PF_KSWAPD)
		return 0;

	key = memory_cgroup_key_for_task(task);
	if (!container_cgroup_key_valid(&key))
		return 0;

	valp = bpf_map_lookup_elem(&memory_cgroup_allocpages_stall, &key);
	if (!valp) {
		struct mem_cgroup_metric new = {
			.directstall_count = 1,
		};
		bpf_map_update_elem(&memory_cgroup_allocpages_stall, &key, &new,
				    COMPAT_BPF_ANY);
		return 0;
	}

	__sync_fetch_and_add(&valp->directstall_count, 1);
	return 0;
}

SEC("kprobe/mem_cgroup_css_released")
int kprobe_mem_cgroup_css_released(struct pt_regs *ctx)
{
	struct container_cgroup_key key = {
		.css = PT_REGS_PARM1(ctx),
	};

	bpf_map_delete_elem(&memory_cgroup_allocpages_stall, &key);
	return 0;
}

SEC("raw_tracepoint/cgroup_rmdir")
int cgroup_rmdir_entry(struct bpf_raw_tracepoint_args *ctx)
{
	struct cgroup *cgroup = (void *)ctx->args[0];
	struct container_cgroup_key key =
		container_cgroup_key_for_default_cgroup(cgroup);

	bpf_map_delete_elem(&memory_cgroup_allocpages_stall, &key);
	return 0;
}

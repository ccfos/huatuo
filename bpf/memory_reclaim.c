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
	__uint(type, BPF_MAP_TYPE_PERCPU_HASH);
	__uint(map_flags, COMPAT_BPF_F_NO_PREALLOC);
	__type(key, u64);
	__type(value, struct mem_cgroup_metric);
	__uint(max_entries, 10241);
} memory_cgroup_allocpages_stall SEC(".maps");

SEC("tracepoint/vmscan/mm_vmscan_memcg_reclaim_begin")
int tracepoint_vmscan_mm_vmscan_memcg_reclaim_begin(struct pt_regs *ctx)
{
	struct cgroup_subsys_state *css;
	struct mem_cgroup_metric *valp;
	struct task_struct *task;
	u64 key = 0;

	task = (struct task_struct *)bpf_get_current_task();
	if (BPF_CORE_READ(task, flags) & PF_KSWAPD)
		return 0;

	/* Non-sleepable tracing writers are CPU-pinned and recursion-protected. */
	valp = bpf_map_lookup_elem(&memory_cgroup_allocpages_stall, &key);
	if (valp)
		valp->directstall_count++;

	css = (struct cgroup_subsys_state *)current_task_memory_css_addr();
	if (!css)
		return 0;
	valp = bpf_map_lookup_elem(&memory_cgroup_allocpages_stall, &css);
	if (!valp) {
		struct mem_cgroup_metric zero = {};
		bpf_map_update_elem(&memory_cgroup_allocpages_stall, &css, &zero,
				    COMPAT_BPF_NOEXIST);
		valp = bpf_map_lookup_elem(&memory_cgroup_allocpages_stall, &css);
	}

	if (valp)
		valp->directstall_count++;
	return 0;
}

SEC("kprobe/mem_cgroup_css_released")
int kprobe_mem_cgroup_css_released(struct pt_regs *ctx)
{
	u64 css = PT_REGS_PARM1(ctx);
	if (css)
		bpf_map_delete_elem(&memory_cgroup_allocpages_stall, &css);
	return 0;
}

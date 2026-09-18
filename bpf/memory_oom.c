#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_ratelimit.h"
#include "abi/oom_types.h"

char __license[] SEC("license") = "Dual MIT/GPL";

BPF_RATELIMIT(rate, BPF_NSEC_PER_SEC, COMPAT_CPU_NUM * 10000);

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} oom_perf_events SEC(".maps");

SEC("kprobe/oom_kill_process")
int BPF_KPROBE(oom_kill_process, struct oom_control *oc, const char *message)
{
	struct oom_event info = {};
	struct task_struct *trigger_task, *victim_task;
	u64 memory_cgrp_id_val;

	if (bpf_ratelimited(&rate))
		return 0;

	if (!oc)
		return 0;

	trigger_task	 = (struct task_struct *)bpf_get_current_task();
	victim_task	 = BPF_CORE_READ(oc, chosen);
	info.trigger_tgid = BPF_CORE_READ(trigger_task, tgid);
	info.victim_tgid  = BPF_CORE_READ(victim_task, tgid);
	BPF_CORE_READ_STR_INTO(&info.trigger_comm, trigger_task, comm);
	BPF_CORE_READ_STR_INTO(&info.victim_comm, victim_task, comm);

	memory_cgrp_id_val =
		bpf_core_enum_value(enum cgroup_subsys_id, memory_cgrp_id);
	info.victim_memcg_css =
	    (u64)BPF_CORE_READ(victim_task, cgroups, subsys[memory_cgrp_id_val]);
	info.trigger_memcg_css =
	    (u64)BPF_CORE_READ(trigger_task, cgroups, subsys[memory_cgrp_id_val]);

	info.mem_limit_pages = BPF_CORE_READ(oc, totalpages);
	struct mem_cgroup *memcg = BPF_CORE_READ(oc, memcg);
	if (memcg) {
		info.mem_usage_pages =
		    (u64)BPF_CORE_READ(memcg, memory.usage.counter);
	}

	bpf_perf_event_output(ctx, &oom_perf_events, COMPAT_BPF_F_CURRENT_CPU,
			      &info, sizeof(info));
	return 0;
}

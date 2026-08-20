#ifndef __BPF_CGROUP_H__
#define __BPF_CGROUP_H__

#include "vmlinux.h"
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

static __always_inline u64 current_task_cpu_css_addr(void)
{
	struct task_struct *task = (struct task_struct *)bpf_get_current_task();
	u64 cpu_cgrp_id_val =
		bpf_core_enum_value(enum cgroup_subsys_id, cpu_cgrp_id);
	return (u64)BPF_CORE_READ(task, cgroups, subsys[cpu_cgrp_id_val]);
}

static __always_inline u64 current_task_memory_css_addr(void)
{
	struct task_struct *task = (struct task_struct *)bpf_get_current_task();
	u64 memory_cgrp_id_val =
		bpf_core_enum_value(enum cgroup_subsys_id, memory_cgrp_id);
	return (u64)BPF_CORE_READ(task, cgroups, subsys[memory_cgrp_id_val]);
}

/* skb_memcg_css_addr returns the memory cgroup subsystem state address for the
 * socket owning skb, or 0 if unavailable. Requires sk_memcg (Linux 5.x+,
 * CONFIG_MEMCG_KMEM). The address equals &sk_memcg->css since css is the
 * first field of struct mem_cgroup. */
static __always_inline u64 sk_memcg_css_addr(struct sock *sk)
{
	if (!sk)
		return 0;

	if (!bpf_core_field_exists(((struct sock *)0)->sk_memcg))
		return 0;

	struct mem_cgroup *memcg = (struct mem_cgroup *)BPF_CORE_READ(sk, sk_memcg);
	if (!memcg)
		return 0;

	return (u64)memcg;
}

static __always_inline u64 skb_memcg_css_addr(struct sk_buff *skb)
{
	return sk_memcg_css_addr(BPF_CORE_READ(skb, sk));
}

#endif

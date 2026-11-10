#ifndef __BPF_CGROUP_H__
#define __BPF_CGROUP_H__

#include "vmlinux.h"
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

struct kernfs_node___css_legacy {
	union {
		u64 id;
	} id;
} __attribute__((preserve_access_index));

struct kernfs_node___css_modern {
	u64 id;
} __attribute__((preserve_access_index));

static __always_inline u64 cgroup_id(struct cgroup *cgrp)
{
	struct kernfs_node *kn;

	if (!cgrp)
		return 0;
	kn = BPF_CORE_READ(cgrp, kn);
	if (!kn)
		return 0;
	/* Older kernels keep the full ID, including generation, in a union. */
	if (bpf_core_field_exists(((struct kernfs_node___css_legacy *)kn)->id.id))
		return BPF_CORE_READ((struct kernfs_node___css_legacy *)kn, id.id);
	if (bpf_core_field_exists(((struct kernfs_node___css_modern *)kn)->id))
		return BPF_CORE_READ((struct kernfs_node___css_modern *)kn, id);
	return 0;
}

static __always_inline u64 cgroup_id_from_css(u64 css_addr)
{
	struct cgroup_subsys_state *css = (void *)css_addr;
	struct cgroup *cgrp;

	if (!css)
		return 0;
	cgrp = BPF_CORE_READ(css, cgroup);
	return cgroup_id(cgrp);
}

static __always_inline bool cgroup_css_matches_ref(u64 observed_css_addr,
						   u64 ref_css_addr,
						   u64 ref_cgroup_id)
{
	return observed_css_addr != 0 && ref_css_addr != 0 &&
	       ref_cgroup_id != 0 && observed_css_addr == ref_css_addr &&
	       cgroup_id_from_css(observed_css_addr) == ref_cgroup_id;
}

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

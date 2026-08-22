#ifndef __BPF_CGROUP_H__
#define __BPF_CGROUP_H__

#include "vmlinux.h"
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

#include "abi/container_cgroup_key.h"

volatile const u64 kernfs_node_id_offset = 0;

static __always_inline bool
container_cgroup_key_valid(const struct container_cgroup_key *key)
{
	return key->cgroup_id || key->css;
}

static __always_inline u64 cgroup_kernfs_id(struct cgroup *cgroup)
{
	struct kernfs_node *kn;
	u64 id = 0;

	if (!cgroup)
		return 0;
	kn = BPF_CORE_READ(cgroup, kn);
	if (!kn)
		return 0;
	bpf_probe_read_kernel(&id, sizeof(id),
			      (const char *)kn + kernfs_node_id_offset);
	return id;
}

/*
 * subsys[] is the effective CSS; dfl_cgrp is the v2 leaf cgroup.
 * On v2, subsys_id only identifies the hierarchy and does not affect cgroup_id.
 * On v1, CSS is controller-specific, so subsys_id must match the userspace
 * container lookup controller.
 */
static __always_inline struct container_cgroup_key
container_cgroup_key_for_task(struct task_struct *task, u64 subsys_id)
{
	struct container_cgroup_key key = {};
	struct css_set *cset;
	struct cgroup_subsys_state *css;
	struct cgroup *css_cgroup;
	struct cgroup *dfl_cgroup;
	struct cgroup_root *css_root;
	struct cgroup_root *dfl_root;

	cset = BPF_CORE_READ(task, cgroups);
	if (!cset)
		return key;

	css = BPF_CORE_READ(cset, subsys[subsys_id]);
	if (!css)
		return key;

	css_cgroup = BPF_CORE_READ(css, cgroup);
	dfl_cgroup = BPF_CORE_READ(cset, dfl_cgrp);
	if (!css_cgroup || !dfl_cgroup)
		return key;
	css_root = BPF_CORE_READ(css_cgroup, root);
	dfl_root = BPF_CORE_READ(dfl_cgroup, root);
	if (css_root == dfl_root)
		key.cgroup_id = cgroup_kernfs_id(dfl_cgroup);
	else
		key.css = (u64)css;

	return key;
}

static __always_inline struct container_cgroup_key
cpu_cgroup_key_for_task(struct task_struct *task)
{
	return container_cgroup_key_for_task(
		task, bpf_core_enum_value(enum cgroup_subsys_id, cpu_cgrp_id));
}

static __always_inline struct container_cgroup_key
memory_cgroup_key_for_task(struct task_struct *task)
{
	return container_cgroup_key_for_task(
		task, bpf_core_enum_value(enum cgroup_subsys_id, memory_cgrp_id));
}

static __always_inline struct container_cgroup_key
container_cgroup_key_for_default_cgroup(struct cgroup *cgroup)
{
	struct container_cgroup_key key = {};

	if (!cgroup || BPF_CORE_READ(cgroup, root, hierarchy_id) != 0)
		return key;

	key.cgroup_id = cgroup_kernfs_id(cgroup);
	return key;
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

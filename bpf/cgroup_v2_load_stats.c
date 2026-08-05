// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

#include "bpf_common.h"
#include "bpf_sched.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define TASK_RUNNING 0
#define TASK_INTERRUPTIBLE 1
#define TASK_UNINTERRUPTIBLE 2
#define __TASK_STOPPED 0x0004
#define TASK_NOLOAD 0x0400
#define TASK_FROZEN 0x8000

struct bpf_iter_meta;

struct bpf_iter__task {
	struct bpf_iter_meta *meta;
	struct task_struct *task;
} __attribute__((preserve_access_index));

struct kernfs_node___id64 {
	u64 id;
} __attribute__((preserve_access_index));

struct cgroup_load_stats {
	u64 nr_sleeping;
	u64 nr_running;
	u64 nr_stopped;
	u64 nr_uninterruptible;
	u64 nr_iowait;
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct cgroup_load_stats);
	__uint(max_entries, 65536);
} cgroup_load_stats SEC(".maps");

enum pid_namespace_status {
	PID_NS_UNCHECKED,
	PID_NS_HOST,
	PID_NS_NESTED,
	PID_NS_READ_ERROR,
};

struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__type(key, u32);
	__type(value, u32);
	__uint(max_entries, 1);
} pid_namespace_status SEC(".maps");

static __always_inline bool collector_in_host_pid_namespace(void)
{
	struct task_struct *current;
	struct pid *pid = NULL;
	u32 key = 0, level = 0;
	u32 *status;

	status = bpf_map_lookup_elem(&pid_namespace_status, &key);
	if (!status)
		return false;
	if (*status != PID_NS_UNCHECKED)
		return *status == PID_NS_HOST;

	/* Check the reader, not the iterated task, which may be in a container. */
	current = (struct task_struct *)bpf_get_current_task();
	if (BPF_CORE_READ_INTO(&pid, current, thread_pid) || !pid ||
	    BPF_CORE_READ_INTO(&level, pid, level)) {
		*status = PID_NS_READ_ERROR;
		return false;
	}
	*status = level == 0 ? PID_NS_HOST : PID_NS_NESTED;
	return *status == PID_NS_HOST;
}

static __always_inline u64 task_cgroup_id(struct task_struct *task)
{
	struct css_set *cgroups;
	struct cgroup *cgroup;
	struct kernfs_node *kn;

	cgroups = BPF_CORE_READ(task, cgroups);
	if (!cgroups)
		return 0;

	cgroup = BPF_CORE_READ(cgroups, dfl_cgrp);
	if (!cgroup)
		return 0;

	kn = BPF_CORE_READ(cgroup, kn);
	if (!kn)
		return 0;

	return BPF_CORE_READ((struct kernfs_node___id64 *)kn, id);
}

SEC("iter/task")
int aggregate_cgroup_load(struct bpf_iter__task *ctx)
{
	struct cgroup_load_stats *stats;
	struct task_struct *task;
	u64 cgroup_id;
	long state;

	/* Also check the terminal callback so an empty traversal cannot pass. */
	if (!collector_in_host_pid_namespace())
		return 0;

	task = ctx->task;
	if (!task)
		return 0;

	cgroup_id = task_cgroup_id(task);
	if (!cgroup_id)
		return 0;

	stats = bpf_map_lookup_elem(&cgroup_load_stats, &cgroup_id);
	if (!stats)
		return 0;

	state = task_state(task);

	/*
	 * __state is a bitmask, so base sleep states can be combined with
	 * modifier bits. Mirror the scheduler's load-contribution rules.
	 */
	if (state == TASK_RUNNING)
		stats->nr_running++;
	else if (state & TASK_INTERRUPTIBLE)
		stats->nr_sleeping++;
	else if ((state & TASK_UNINTERRUPTIBLE) &&
		 !(state & (TASK_NOLOAD | TASK_FROZEN)))
		stats->nr_uninterruptible++;
	else if (state & __TASK_STOPPED)
		stats->nr_stopped++;

	if (BPF_CORE_READ_BITFIELD_PROBED(task, in_iowait))
		stats->nr_iowait++;

	return 0;
}

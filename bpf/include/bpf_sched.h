#ifndef __BPF_SCHED_H__
#define __BPF_SCHED_H__

#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

struct task_struct___5_14 {
	unsigned int __state;
} __attribute__((preserve_access_index));

static __always_inline long task_state(struct task_struct *task)
{
	struct task_struct___5_14 *new_task;

	if (!task)
		return -1;

	if (bpf_core_field_exists(task->state))
		return BPF_CORE_READ(task, state);

	new_task = (void *)task;
	return (long)BPF_CORE_READ(new_task, __state);
}

#endif /* __BPF_SCHED_H__ */

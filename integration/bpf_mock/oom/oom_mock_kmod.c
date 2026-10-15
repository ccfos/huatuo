// SPDX-License-Identifier: GPL-2.0
// Copyright 2026 The HuaTuo Authors.

#include <linux/fs.h>
#include <linux/kernel.h>
#include <linux/memcontrol.h>
#include <linux/miscdevice.h>
#include <linux/module.h>
#include <linux/oom.h>
#include <linux/sched.h>
#include <linux/slab.h>
#include <linux/uaccess.h>

#include "oom_mock.h"

#ifndef CONFIG_MEMCG
#error "OOM fixture requires CONFIG_MEMCG, as does the production BPF program"
#endif

/* Keep a separate symbol with the entry ABI of oom_kill_process(). */
void oom_kill_process_mock(struct oom_control *oc, const char *message);

void noinline __noclone oom_kill_process_mock(struct oom_control *oc,
					      const char *message)
{
	asm volatile("" : : "r"(oc), "r"(message) : "memory");
}

static long run_oom_scenario(struct oom_mock_request *request)
{
	struct task_struct *victim = NULL;
	struct mem_cgroup *memcg = NULL;
	struct task_struct *mock_task = NULL;
	struct css_set *mock_cgroups = NULL;
	struct cgroup_subsys_state *mock_css = NULL;
	struct pid *pid;
	struct oom_control oc = {
		.totalpages = request->total_pages,
	};
	long result = 0;

	if (request->scenario > OOM_MOCK_POINTER_CHAIN || !request->total_pages ||
	    request->usage_pages > LONG_MAX)
		return -EINVAL;

	if (request->scenario == OOM_MOCK_OTHER_TASK ||
	    request->scenario == OOM_MOCK_OTHER_TASK_MEMCG) {
		pid = find_get_pid(request->victim_pid);
		victim = get_pid_task(pid, PIDTYPE_PID);
		put_pid(pid);
		if (!victim)
			return -ESRCH;
		oc.chosen = victim;
	} else if (request->scenario == OOM_MOCK_POINTER_CHAIN) {
		/* Distinct CSS addresses detect reading the trigger's chain for the victim. */
		mock_task = kzalloc(sizeof(*mock_task), GFP_KERNEL);
		mock_cgroups = kzalloc(sizeof(*mock_cgroups), GFP_KERNEL);
		mock_css = kzalloc(sizeof(*mock_css), GFP_KERNEL);
		if (!mock_task || !mock_cgroups || !mock_css) {
			result = -ENOMEM;
			goto out;
		}
		mock_task->tgid = 4242;
		strcpy(mock_task->comm, "oom-mock-victim");
		mock_cgroups->subsys[memory_cgrp_id] = mock_css;
		rcu_assign_pointer(mock_task->cgroups, mock_cgroups);
		oc.chosen = mock_task;
	} else if (request->scenario != OOM_MOCK_NULL_VICTIM) {
		oc.chosen = current;
	}

	if (request->scenario == OOM_MOCK_MEMCG ||
	    request->scenario == OOM_MOCK_OTHER_TASK_MEMCG ||
	    request->scenario == OOM_MOCK_POINTER_CHAIN) {
		/* Only BPF reads this counter; never charge or alter a real cgroup. */
		memcg = kzalloc(sizeof(*memcg), GFP_KERNEL);
		if (!memcg) {
			result = -ENOMEM;
			goto out;
		}
		atomic_long_set(&memcg->memory.usage, request->usage_pages);
		oc.memcg = memcg;
	}

	request->trigger_tgid = current->tgid;
	request->victim_tgid = oc.chosen ? oc.chosen->tgid : 0;
	rcu_read_lock();
	request->trigger_css = (unsigned long)task_css(current, memory_cgrp_id);
	request->victim_css = oc.chosen ?
		(unsigned long)task_css(oc.chosen, memory_cgrp_id) : 0;
	rcu_read_unlock();
	oom_kill_process_mock(request->scenario == OOM_MOCK_NULL_CONTROL ?
			      NULL : &oc, "huatuo integration test");
out:
	kfree(memcg);
	kfree(mock_css);
	kfree(mock_cgroups);
	kfree(mock_task);
	if (victim)
		put_task_struct(victim);
	return result;
}

static long huatuo_oom_mock_ioctl(struct file *file, unsigned int command,
				  unsigned long argument)
{
	struct oom_mock_request request;
	long result;

	if (command != HUATUO_OOM_MOCK_SCENARIO)
		return -ENOTTY;
	if (copy_from_user(&request, (void __user *)argument, sizeof(request)))
		return -EFAULT;
	result = run_oom_scenario(&request);
	if (result)
		return result;
	if (copy_to_user((void __user *)argument, &request, sizeof(request)))
		return -EFAULT;
	return 0;
}

static const struct file_operations huatuo_oom_mock_fops = {
	.owner = THIS_MODULE,
	.unlocked_ioctl = huatuo_oom_mock_ioctl,
};

static struct miscdevice huatuo_oom_mock_device = {
	.minor = MISC_DYNAMIC_MINOR,
	.name = "huatuo_oom_mock",
	.fops = &huatuo_oom_mock_fops,
	.mode = 0600,
};

static int __init huatuo_oom_mock_init(void)
{
	return misc_register(&huatuo_oom_mock_device);
}

static void __exit huatuo_oom_mock_exit(void)
{
	misc_deregister(&huatuo_oom_mock_device);
}

module_init(huatuo_oom_mock_init);
module_exit(huatuo_oom_mock_exit);

MODULE_LICENSE("GPL");
MODULE_DESCRIPTION("Huatuo OOM kprobe integration fixture");

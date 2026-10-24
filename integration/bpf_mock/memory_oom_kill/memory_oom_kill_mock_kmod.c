// SPDX-License-Identifier: (GPL-2.0 OR Apache-2.0)
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

#include "memory_oom_kill_mock.h"

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

/* Immutable synthetic objects live for the module's lifetime. */
static struct cgroup_subsys_state mock_css;
static struct css_set mock_cgroups = {
    .subsys[memory_cgrp_id] = &mock_css,
};
static struct task_struct mock_victim = {
    .tgid = OOM_MOCK_SYNTHETIC_PID,
    .comm = "oom-mock-victim",
};

static long get_oom_victim(unsigned int scenario, unsigned int victim_pid,
			   struct task_struct **victim)
{
	struct pid *pid;

	if (scenario > OOM_MOCK_POINTER_CHAIN)
		return -EINVAL;
	switch (scenario) {
	case OOM_MOCK_OTHER_TASK:
	case OOM_MOCK_OTHER_TASK_MEMCG:
		pid = find_get_pid(victim_pid);
		*victim = get_pid_task(pid, PIDTYPE_PID);
		put_pid(pid);
		return *victim ? 0 : -ESRCH;
	case OOM_MOCK_POINTER_CHAIN:
		*victim = &mock_victim;
		break;
	case OOM_MOCK_NULL_VICTIM:
		*victim = NULL;
		break;
	default:
		*victim = current;
		get_task_struct(*victim);
		break;
	}
	return 0;
}

static void put_oom_victim(struct task_struct *victim)
{
	if (victim && victim != &mock_victim)
		put_task_struct(victim);
}

static long get_oom_css(struct oom_mock_css_query *query)
{
	struct task_struct *victim;
	long result;

	result = get_oom_victim(query->scenario, query->victim_pid, &victim);
	if (result)
		return result;
	rcu_read_lock();
	query->trigger_css = (unsigned long)task_css(current, memory_cgrp_id);
	query->victim_css =
	    victim ? (unsigned long)task_css(victim, memory_cgrp_id) : 0;
	rcu_read_unlock();
	put_oom_victim(victim);
	return 0;
}

static long run_oom_scenario(const struct oom_mock_request *request)
{
	struct mem_cgroup *memcg = NULL;
	struct oom_control oc = {
	    .totalpages = request->total_pages,
	};
	long result;

	if (!request->total_pages || request->usage_pages > LONG_MAX)
		return -EINVAL;
	result =
	    get_oom_victim(request->scenario, request->victim_pid, &oc.chosen);
	if (result)
		return result;
	if (request->scenario == OOM_MOCK_MEMCG ||
	    request->scenario == OOM_MOCK_OTHER_TASK_MEMCG ||
	    request->scenario == OOM_MOCK_POINTER_CHAIN) {
		/* Only BPF reads this counter; never charge or alter a real
		 * cgroup. */
		memcg = kzalloc(sizeof(*memcg), GFP_KERNEL);
		if (!memcg) {
			result = -ENOMEM;
			goto out;
		}
		atomic_long_set(&memcg->memory.usage, request->usage_pages);
		oc.memcg = memcg;
	}

	oom_kill_process_mock(request->scenario == OOM_MOCK_NULL_CONTROL ? NULL
									 : &oc,
			      "huatuo integration test");
out:
	kfree(memcg);
	put_oom_victim(oc.chosen);
	return result;
}

static long memory_oom_kill_mock_ioctl(struct file *file, unsigned int command,
				       unsigned long argument)
{
	struct oom_mock_css_query query;
	struct oom_mock_request request;
	long result;

	switch (command) {
	case HUATUO_OOM_MOCK_GET_CSS:
		if (copy_from_user(&query, (void __user *)argument,
				   sizeof(query)))
			return -EFAULT;
		result = get_oom_css(&query);
		if (result)
			return result;
		if (copy_to_user((void __user *)argument, &query,
				 sizeof(query)))
			return -EFAULT;
		return 0;
	case HUATUO_OOM_MOCK_RUN:
		if (copy_from_user(&request, (void __user *)argument,
				   sizeof(request)))
			return -EFAULT;
		return run_oom_scenario(&request);
	default:
		return -ENOTTY;
	}
}

static const struct file_operations mock_fops = {
    .owner = THIS_MODULE,
    .unlocked_ioctl = memory_oom_kill_mock_ioctl,
};

static struct miscdevice mock_device = {
    .minor = MISC_DYNAMIC_MINOR,
    .name = "memory_oom_kill_mock",
    .fops = &mock_fops,
    .mode = 0600,
};

static int __init memory_oom_kill_mock_init(void)
{
	RCU_INIT_POINTER(mock_victim.cgroups, &mock_cgroups);
	return misc_register(&mock_device);
}

static void __exit memory_oom_kill_mock_exit(void)
{
	misc_deregister(&mock_device);
}

module_init(memory_oom_kill_mock_init);
module_exit(memory_oom_kill_mock_exit);

MODULE_LICENSE("GPL");
MODULE_DESCRIPTION("Huatuo OOM kprobe integration fixture");

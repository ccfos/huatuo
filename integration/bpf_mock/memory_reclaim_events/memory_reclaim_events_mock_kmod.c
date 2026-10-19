// SPDX-License-Identifier: (GPL-2.0 OR Apache-2.0)
// Copyright 2026 The HuaTuo Authors.

#include <linux/delay.h>
#include <linux/fs.h>
#include <linux/gfp.h>
#include <linux/miscdevice.h>
#include <linux/module.h>
#include <linux/nodemask.h>
#include <linux/uaccess.h>

#include "memory_reclaim_events_mock.h"
#include "memory_reclaim_events_hook.h"

unsigned long try_to_free_pages_mock RECLAIM_MOCK_HOOK_ARGS;

unsigned long noinline __noclone try_to_free_pages_mock RECLAIM_MOCK_HOOK_ARGS
{
	/* Exercise entry/return timing without reclaiming any pages. */
	msleep(RECLAIM_MOCK_DELAY_MS);
	return 0;
}

static long run(const struct reclaim_mock_request *request)
{

	if (request->scenario != RECLAIM_MOCK_CURRENT_TASK)
		return -EINVAL;

	try_to_free_pages_mock(node_zonelist(numa_node_id(), GFP_KERNEL), 0,
			       GFP_KERNEL, NULL);
	return 0;
}

static long mock_ioctl(struct file *file, unsigned int cmd, unsigned long arg)
{
	struct reclaim_mock_request request;

	if (cmd != HUATUO_RECLAIM_MOCK_RUN)
		return -ENOTTY;
	if (copy_from_user(&request, (void __user *)arg, sizeof(request)))
		return -EFAULT;
	return run(&request);
}

static const struct file_operations mock_fops = {
    .owner = THIS_MODULE,
    .unlocked_ioctl = mock_ioctl,
};

static struct miscdevice mock_device = {
    .minor = MISC_DYNAMIC_MINOR,
    .name = "memory_reclaim_events_mock",
    .fops = &mock_fops,
    .mode = 0600,
};

module_misc_device(mock_device);

MODULE_LICENSE("GPL");
MODULE_DESCRIPTION("Huatuo direct reclaim kprobe and kretprobe fixture");

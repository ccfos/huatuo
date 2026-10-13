// SPDX-License-Identifier: GPL-2.0
// Copyright 2026 The HuaTuo Authors.

#include <linux/fs.h>
#include <linux/miscdevice.h>
#include <linux/module.h>

#include "hungtask_mock.h"

#if !defined(CONFIG_TRACEPOINTS) || !defined(CONFIG_EVENT_TRACING)
#error "hung task fixture requires CONFIG_TRACEPOINTS and CONFIG_EVENT_TRACING"
#endif

#define CREATE_TRACE_POINTS
#include "hungtask_trace.h"

static long huatuo_hungtask_mock_ioctl(struct file *file, unsigned int command,
				     unsigned long argument)
{
	if (command != HUATUO_HUNGTASK_MOCK_RUN)
		return -ENOTTY;
	if (argument)
		return -EINVAL;

	/* Emit the production event layout without putting the task in D state. */
	trace_sched_process_hang_mock(current);
	return 0;
}

static const struct file_operations huatuo_hungtask_mock_fops = {
	.owner = THIS_MODULE,
	.unlocked_ioctl = huatuo_hungtask_mock_ioctl,
};

static struct miscdevice huatuo_hungtask_mock_device = {
	.minor = MISC_DYNAMIC_MINOR,
	.name = "huatuo_hungtask_mock",
	.fops = &huatuo_hungtask_mock_fops,
	.mode = 0600,
};

static int __init huatuo_hungtask_mock_init(void)
{
	return misc_register(&huatuo_hungtask_mock_device);
}

static void __exit huatuo_hungtask_mock_exit(void)
{
	misc_deregister(&huatuo_hungtask_mock_device);
}

module_init(huatuo_hungtask_mock_init);
module_exit(huatuo_hungtask_mock_exit);

MODULE_LICENSE("GPL");
MODULE_DESCRIPTION("Huatuo hung task tracepoint integration fixture");

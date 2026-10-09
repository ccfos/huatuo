// SPDX-License-Identifier: GPL-2.0
// Copyright 2026 The HuaTuo Authors.

#include <linux/fs.h>
#include <linux/miscdevice.h>
#include <linux/module.h>
#include <linux/oom.h>
#include <linux/sched.h>

#include "oom_mock.h"

/* Keep a separate symbol with the entry ABI of oom_kill_process(). */
void oom_kill_process_mock(struct oom_control *oc, const char *message);

void noinline __noclone oom_kill_process_mock(struct oom_control *oc,
					      const char *message)
{
	asm volatile("" : : "r"(oc), "r"(message) : "memory");
}

static long huatuo_oom_mock_ioctl(struct file *file, unsigned int command,
				  unsigned long argument)
{
	struct oom_control oc = {
		.chosen = current,
		.totalpages = argument,
	};

	if (command != HUATUO_OOM_MOCK_RUN)
		return -ENOTTY;
	if (!argument)
		return -EINVAL;

	oom_kill_process_mock(&oc, "huatuo integration test");
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

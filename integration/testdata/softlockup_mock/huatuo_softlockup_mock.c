// SPDX-License-Identifier: GPL-2.0
// Copyright 2026 The HuaTuo Authors.

#include <linux/fs.h>
#include <linux/kernel.h>
#include <linux/miscdevice.h>
#include <linux/module.h>

#include "softlockup_mock.h"

/* Preserve add_taint()'s entry ABI without changing the kernel taint state. */
void add_taint_mock(unsigned flag, enum lockdep_ok lockdep_ok);

void noinline __noclone add_taint_mock(unsigned flag, enum lockdep_ok lockdep_ok)
{
	asm volatile("" : : "r"(flag), "r"(lockdep_ok) : "memory");
}

static long huatuo_softlockup_mock_ioctl(struct file *file, unsigned int command,
				       unsigned long argument)
{
	unsigned flag;

	if (command != HUATUO_SOFTLOCKUP_MOCK_RUN)
		return -ENOTTY;

	switch (argument) {
	case SOFTLOCKUP_MOCK_TAINT_SOFTLOCKUP:
		flag = TAINT_SOFTLOCKUP;
		break;
	case SOFTLOCKUP_MOCK_TAINT_USER:
		flag = TAINT_USER;
		break;
	default:
		return -EINVAL;
	}

	add_taint_mock(flag, LOCKDEP_STILL_OK);
	return 0;
}

static const struct file_operations huatuo_softlockup_mock_fops = {
	.owner = THIS_MODULE,
	.unlocked_ioctl = huatuo_softlockup_mock_ioctl,
};

static struct miscdevice huatuo_softlockup_mock_device = {
	.minor = MISC_DYNAMIC_MINOR,
	.name = "huatuo_softlockup_mock",
	.fops = &huatuo_softlockup_mock_fops,
	.mode = 0600,
};

static int __init huatuo_softlockup_mock_init(void)
{
	return misc_register(&huatuo_softlockup_mock_device);
}

static void __exit huatuo_softlockup_mock_exit(void)
{
	misc_deregister(&huatuo_softlockup_mock_device);
}

module_init(huatuo_softlockup_mock_init);
module_exit(huatuo_softlockup_mock_exit);

MODULE_LICENSE("GPL");
MODULE_DESCRIPTION("Huatuo softlockup kprobe integration fixture");

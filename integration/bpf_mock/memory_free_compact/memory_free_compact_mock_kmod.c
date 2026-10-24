// SPDX-License-Identifier: (GPL-2.0 OR Apache-2.0)
// Copyright 2026 The HuaTuo Authors.

#include <linux/compaction.h>
#include <linux/delay.h>
#include <linux/fs.h>
#include <linux/miscdevice.h>
#include <linux/mm.h>
#include <linux/module.h>
#include <linux/uaccess.h>

#include "memory_free_compact_mock.h"

#include "memory_free_compact_hook.h"
#define CREATE_TRACE_POINTS
#include "memory_free_compact_trace.h"

static long run(const struct memory_free_compact_request *request)
{
	switch (request->scenario) {
	case FREE_COMPACT_RECLAIM:
		CALL_RECLAIM_BEGIN();
		msleep(FREE_COMPACT_DELAY_MS);
		CALL_RECLAIM_END();
		break;
	case FREE_COMPACT_COMPACTION:
		CALL_try_to_compact_pages();
		break;
	case FREE_COMPACT_UNMATCHED_END:
		CALL_RECLAIM_END();
		break;
	default:
		return -EINVAL;
	}
	return 0;
}

static long mock_ioctl(struct file *file, unsigned int cmd, unsigned long arg)
{
	struct memory_free_compact_request request;

	if (cmd != MEMORY_FREE_COMPACT_RUN)
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
    .name = "memory_free_compact_mock",
    .fops = &mock_fops,
    .mode = 0600,
};

module_misc_device(mock_device);

MODULE_LICENSE("GPL");
MODULE_DESCRIPTION("Huatuo memory_free_compact integration fixture");

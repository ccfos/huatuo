// SPDX-License-Identifier: (GPL-2.0 OR Apache-2.0)
// Copyright 2026 The HuaTuo Authors.

#include <linux/fs.h>
#include <linux/miscdevice.h>
#include <linux/mm.h>
#include <linux/module.h>
#include <linux/slab.h>
#include <linux/uaccess.h>

#include "native_virtual_alloc_hook.h"
#include "native_virtual_alloc_mock.h"

static long run(const struct native_virtual_alloc_request *request)
{
	struct file *mock_file;
	if (request->scenario > NATIVE_VIRTUAL_ALLOC_FILE_ONLY)
		return -EINVAL;
	mock_file = kzalloc(sizeof(*mock_file), GFP_KERNEL);
	if (!mock_file)
		return -ENOMEM;
	CALL_do_mmap(mock_file, 0, 16384);
	if (request->scenario == NATIVE_VIRTUAL_ALLOC_MIXED) {
		CALL_do_mmap(NULL, 0, 4096);
		CALL_do_mmap(NULL, 0, 8192);
		CALL_do_mmap(NULL, 0, 0);
	}
	kfree(mock_file);
	return 0;
}

static long mock_ioctl(struct file *file, unsigned int cmd, unsigned long arg)
{
	struct native_virtual_alloc_request request;

	if (cmd != NATIVE_VIRTUAL_ALLOC_RUN)
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
    .name = "native_virtual_alloc_mock",
    .fops = &mock_fops,
    .mode = 0600,
};

module_misc_device(mock_device);

MODULE_LICENSE("GPL");
MODULE_DESCRIPTION("Huatuo native_virtual_alloc integration fixture");

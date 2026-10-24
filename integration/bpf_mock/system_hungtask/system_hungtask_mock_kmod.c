// SPDX-License-Identifier: (GPL-2.0 OR Apache-2.0)
// Copyright 2026 The HuaTuo Authors.

#include <linux/fs.h>
#include <linux/miscdevice.h>
#include <linux/module.h>
#include <linux/sched.h>
#include <linux/uaccess.h>
#include <linux/workqueue.h>

#include "system_hungtask_mock.h"

#if !defined(CONFIG_TRACEPOINTS) || !defined(CONFIG_EVENT_TRACING)
#error "hung task fixture requires CONFIG_TRACEPOINTS and CONFIG_EVENT_TRACING"
#endif

#define CREATE_TRACE_POINTS
#include "system_hungtask_trace.h"

/* Production's detector reports another task, not its own current task. */
struct hungtask_report {
	struct work_struct work;
	struct task_struct *task;
};

static void report_hungtask(struct work_struct *work)
{
	struct hungtask_report *report =
	    container_of(work, struct hungtask_report, work);
	trace_sched_process_hang_mock(report->task);
}

static long run(const struct hungtask_mock_request *request)
{
	struct hungtask_report report = {.task = current};
	if (request->scenario != HUNGTASK_MOCK_CURRENT_TASK)
		return -EINVAL;
	/* The ioctl keeps the reported task alive until the worker has
	 * finished. */
	INIT_WORK(&report.work, report_hungtask);
	schedule_work(&report.work);
	flush_work(&report.work);
	return 0;
}

static long mock_ioctl(struct file *file, unsigned int cmd, unsigned long arg)
{
	struct hungtask_mock_request request;

	if (cmd != HUATUO_HUNGTASK_MOCK_RUN)
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
    .name = "system_hungtask_mock",
    .fops = &mock_fops,
    .mode = 0600,
};

module_misc_device(mock_device);

MODULE_LICENSE("GPL");
MODULE_DESCRIPTION("Huatuo hung task tracepoint integration fixture");

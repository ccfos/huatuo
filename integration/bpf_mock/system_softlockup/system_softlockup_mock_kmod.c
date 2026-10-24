// SPDX-License-Identifier: (GPL-2.0 OR Apache-2.0)
// Copyright 2026 The HuaTuo Authors.

#include <linux/fs.h>
#include <linux/interrupt.h>
#include <linux/irq_work.h>
#include <linux/kernel.h>
#include <linux/ktime.h>
#include <linux/miscdevice.h>
#include <linux/module.h>
#include <linux/preempt.h>
#include <linux/uaccess.h>

#include "system_softlockup_mock.h"

/* Preserve add_taint()'s entry ABI without changing the kernel taint state. */
void add_taint_mock(unsigned flag, enum lockdep_ok lockdep_ok);

void noinline __noclone add_taint_mock(unsigned flag,
				       enum lockdep_ok lockdep_ok)
{
	asm volatile("" : : "r"(flag), "r"(lockdep_ok) : "memory");
}

struct softlockup_report {
	struct irq_work work;
	unsigned flag;
	int result;
	bool done;
};

static void report_softlockup(struct irq_work *work)
{
	struct softlockup_report *report =
	    container_of(work, struct softlockup_report, work);
	report->result = hardirq_count() ? 0 : -EOPNOTSUPP;
	if (!report->result)
		add_taint_mock(report->flag, LOCKDEP_STILL_OK);
	WRITE_ONCE(report->done, true);
}

static long run(const struct softlockup_mock_request *request)
{
	struct softlockup_report report = {};
	u64 deadline;
	bool completed;
	switch (request->scenario) {
	case SOFTLOCKUP_MOCK_TAINT_SOFTLOCKUP:
		report.flag = TAINT_SOFTLOCKUP;
		break;
	case SOFTLOCKUP_MOCK_TAINT_USER:
		report.flag = TAINT_USER;
		break;
	default:
		return -EINVAL;
	}
#ifdef IRQ_WORK_INIT_HARD
	report.work = (struct irq_work)IRQ_WORK_INIT_HARD(report_softlockup);
#else
	/* Older non-RT kernels execute ordinary irq_work in hardirq context. */
	if (IS_ENABLED(CONFIG_PREEMPT_RT) || IS_ENABLED(CONFIG_PREEMPT_RT_FULL))
		return -EOPNOTSUPP;
	init_irq_work(&report.work, report_softlockup);
#endif
	/* Keep the trigger current while the interrupt samples its identity. */
	preempt_disable();
	deadline = ktime_get_ns() + NSEC_PER_SEC;
	irq_work_queue(&report.work);
	while (!READ_ONCE(report.done) && ktime_get_ns() < deadline)
		cpu_relax();
	completed = READ_ONCE(report.done);
	preempt_enable();
	irq_work_sync(&report.work);
	return completed ? report.result : -ETIMEDOUT;
}

static long mock_ioctl(struct file *file, unsigned int cmd, unsigned long arg)
{
	struct softlockup_mock_request request;

	if (cmd != HUATUO_SOFTLOCKUP_MOCK_RUN)
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
    .name = "system_softlockup_mock",
    .fops = &mock_fops,
    .mode = 0600,
};

module_misc_device(mock_device);

MODULE_LICENSE("GPL");
MODULE_DESCRIPTION("Huatuo softlockup kprobe integration fixture");

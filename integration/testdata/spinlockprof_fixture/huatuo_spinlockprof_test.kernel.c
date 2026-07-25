// SPDX-License-Identifier: GPL-2.0
/* Deterministic spinlock contention fixture for the Huatuo integration test. */

#include <linux/delay.h>
#include <linux/fs.h>
#include <linux/ioctl.h>
#include <linux/miscdevice.h>
#include <linux/module.h>
#include <linux/spinlock.h>

#define HUATUO_SPINLOCKPROF_IOC_MAGIC 0xb8
#define HUATUO_SPINLOCKPROF_CONTEND _IO(HUATUO_SPINLOCKPROF_IOC_MAGIC, 1)

static DEFINE_SPINLOCK(fixture_spinlock);

static long huatuo_spinlockprof_ioctl(struct file *file, unsigned int cmd,
				      unsigned long arg)
{
	(void)file;
	(void)arg;

	if (cmd != HUATUO_SPINLOCKPROF_CONTEND)
		return -ENOTTY;

	spin_lock(&fixture_spinlock);
	udelay(25);
	spin_unlock(&fixture_spinlock);
	return 0;
}

static const struct file_operations huatuo_spinlockprof_fops = {
	.owner = THIS_MODULE,
	.unlocked_ioctl = huatuo_spinlockprof_ioctl,
#ifdef CONFIG_COMPAT
	.compat_ioctl = huatuo_spinlockprof_ioctl,
#endif
};

static struct miscdevice huatuo_spinlockprof_device = {
	.minor = MISC_DYNAMIC_MINOR,
	.name = "huatuo_spinlockprof_fixture",
	.fops = &huatuo_spinlockprof_fops,
	.mode = 0600,
};

static int __init huatuo_spinlockprof_init(void)
{
	return misc_register(&huatuo_spinlockprof_device);
}

static void __exit huatuo_spinlockprof_exit(void)
{
	misc_deregister(&huatuo_spinlockprof_device);
}

module_init(huatuo_spinlockprof_init);
module_exit(huatuo_spinlockprof_exit);

MODULE_AUTHOR("The HuaTuo Authors");
MODULE_DESCRIPTION("Kernel spinlock contention integration fixture");
MODULE_LICENSE("GPL");

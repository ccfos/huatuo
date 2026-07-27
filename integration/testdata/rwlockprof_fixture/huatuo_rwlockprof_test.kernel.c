// SPDX-License-Identifier: GPL-2.0
/* Deterministic rwlock contention fixture for the Huatuo integration test. */

#include <linux/delay.h>
#include <linux/fs.h>
#include <linux/ioctl.h>
#include <linux/miscdevice.h>
#include <linux/module.h>
#include <linux/rwlock.h>

#define HUATUO_RWLOCKPROF_IOC_MAGIC 0xb8
#define HUATUO_RWLOCKPROF_READ _IO(HUATUO_RWLOCKPROF_IOC_MAGIC, 1)
#define HUATUO_RWLOCKPROF_WRITE _IO(HUATUO_RWLOCKPROF_IOC_MAGIC, 2)

static DEFINE_RWLOCK(fixture_rwlock);

static long huatuo_rwlockprof_ioctl(struct file *file, unsigned int cmd,
				    unsigned long arg)
{
	(void)file;
	(void)arg;

	switch (cmd) {
	case HUATUO_RWLOCKPROF_READ:
		read_lock(&fixture_rwlock);
		udelay(75);
		read_unlock(&fixture_rwlock);
		return 0;
	case HUATUO_RWLOCKPROF_WRITE:
		write_lock(&fixture_rwlock);
		udelay(75);
		write_unlock(&fixture_rwlock);
		return 0;
	default:
		return -ENOTTY;
	}
}

static const struct file_operations huatuo_rwlockprof_fops = {
	.owner = THIS_MODULE,
	.unlocked_ioctl = huatuo_rwlockprof_ioctl,
#ifdef CONFIG_COMPAT
	.compat_ioctl = huatuo_rwlockprof_ioctl,
#endif
};

static struct miscdevice huatuo_rwlockprof_device = {
	.minor = MISC_DYNAMIC_MINOR,
	.name = "huatuo_rwlockprof_fixture",
	.fops = &huatuo_rwlockprof_fops,
	.mode = 0600,
};

static int __init huatuo_rwlockprof_init(void)
{
	return misc_register(&huatuo_rwlockprof_device);
}

static void __exit huatuo_rwlockprof_exit(void)
{
	misc_deregister(&huatuo_rwlockprof_device);
}

module_init(huatuo_rwlockprof_init);
module_exit(huatuo_rwlockprof_exit);

MODULE_AUTHOR("The HuaTuo Authors");
MODULE_DESCRIPTION("Kernel rwlock contention integration fixture");
MODULE_LICENSE("GPL");

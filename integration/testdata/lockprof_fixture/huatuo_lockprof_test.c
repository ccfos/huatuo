// SPDX-License-Identifier: GPL-2.0
/* Deterministic kernel lock contention fixture for the Huatuo integration test. */

#include <linux/delay.h>
#include <linux/fs.h>
#include <linux/ioctl.h>
#include <linux/miscdevice.h>
#include <linux/module.h>
#include <linux/mutex.h>
#include <linux/rwlock.h>
#include <linux/spinlock.h>

#define HUATUO_LOCKPROF_IOC_MAGIC 0xb7
#define HUATUO_LOCKPROF_MUTEX _IO(HUATUO_LOCKPROF_IOC_MAGIC, 1)
#define HUATUO_LOCKPROF_SPIN _IO(HUATUO_LOCKPROF_IOC_MAGIC, 2)
#define HUATUO_LOCKPROF_RW_READ _IO(HUATUO_LOCKPROF_IOC_MAGIC, 3)
#define HUATUO_LOCKPROF_RW_WRITE _IO(HUATUO_LOCKPROF_IOC_MAGIC, 4)

static DEFINE_MUTEX(fixture_mutex);
static DEFINE_SPINLOCK(fixture_spinlock);
static DEFINE_RWLOCK(fixture_rwlock);

static long huatuo_lockprof_ioctl(struct file *file, unsigned int cmd,
				  unsigned long arg)
{
	(void)file;
	(void)arg;

	switch (cmd) {
	case HUATUO_LOCKPROF_MUTEX:
		mutex_lock(&fixture_mutex);
		usleep_range(1000, 1500);
		mutex_unlock(&fixture_mutex);
		return 0;
	case HUATUO_LOCKPROF_SPIN:
		spin_lock(&fixture_spinlock);
		udelay(100);
		spin_unlock(&fixture_spinlock);
		return 0;
	case HUATUO_LOCKPROF_RW_READ:
		read_lock(&fixture_rwlock);
		udelay(50);
		read_unlock(&fixture_rwlock);
		return 0;
	case HUATUO_LOCKPROF_RW_WRITE:
		write_lock(&fixture_rwlock);
		udelay(100);
		write_unlock(&fixture_rwlock);
		return 0;
	default:
		return -ENOTTY;
	}
}

static const struct file_operations huatuo_lockprof_fops = {
	.owner = THIS_MODULE,
	.unlocked_ioctl = huatuo_lockprof_ioctl,
#ifdef CONFIG_COMPAT
	.compat_ioctl = huatuo_lockprof_ioctl,
#endif
};

static struct miscdevice huatuo_lockprof_device = {
	.minor = MISC_DYNAMIC_MINOR,
	.name = "huatuo_lockprof_fixture",
	.fops = &huatuo_lockprof_fops,
	.mode = 0600,
};

static int __init huatuo_lockprof_init(void)
{
	return misc_register(&huatuo_lockprof_device);
}

static void __exit huatuo_lockprof_exit(void)
{
	misc_deregister(&huatuo_lockprof_device);
}

module_init(huatuo_lockprof_init);
module_exit(huatuo_lockprof_exit);

MODULE_AUTHOR("The HuaTuo Authors");
MODULE_DESCRIPTION("Kernel lock contention integration fixture");
MODULE_LICENSE("GPL");

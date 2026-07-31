// SPDX-License-Identifier: GPL-2.0
/* Deterministic mutex contention fixture for the Huatuo integration test. */

#include <linux/delay.h>
#include <linux/fs.h>
#include <linux/ioctl.h>
#include <linux/miscdevice.h>
#include <linux/module.h>
#include <linux/mutex.h>

#define HUATUO_MUTEXPROF_IOC_MAGIC 0xb7
#define HUATUO_MUTEXPROF_CONTEND _IO(HUATUO_MUTEXPROF_IOC_MAGIC, 1)

static DEFINE_MUTEX(fixture_mutex);

static long huatuo_mutexprof_ioctl(struct file *file, unsigned int cmd,
				   unsigned long arg)
{
	(void)file;
	(void)arg;

	if (cmd != HUATUO_MUTEXPROF_CONTEND)
		return -ENOTTY;

	mutex_lock(&fixture_mutex);
	usleep_range(1000, 1500);
	mutex_unlock(&fixture_mutex);
	return 0;
}

static const struct file_operations huatuo_mutexprof_fops = {
	.owner = THIS_MODULE,
	.unlocked_ioctl = huatuo_mutexprof_ioctl,
#ifdef CONFIG_COMPAT
	.compat_ioctl = huatuo_mutexprof_ioctl,
#endif
};

static struct miscdevice huatuo_mutexprof_device = {
	.minor = MISC_DYNAMIC_MINOR,
	.name = "huatuo_mutexprof_fixture",
	.fops = &huatuo_mutexprof_fops,
	.mode = 0666,
};

static int __init huatuo_mutexprof_init(void)
{
	return misc_register(&huatuo_mutexprof_device);
}

static void __exit huatuo_mutexprof_exit(void)
{
	misc_deregister(&huatuo_mutexprof_device);
}

module_init(huatuo_mutexprof_init);
module_exit(huatuo_mutexprof_exit);

MODULE_AUTHOR("The HuaTuo Authors");
MODULE_DESCRIPTION("Kernel mutex contention integration fixture");
MODULE_LICENSE("GPL");

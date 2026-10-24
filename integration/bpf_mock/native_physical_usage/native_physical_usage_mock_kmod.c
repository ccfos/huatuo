// SPDX-License-Identifier: (GPL-2.0 OR Apache-2.0)
// Copyright 2026 The HuaTuo Authors.

#include <linux/fs.h>
#include <linux/miscdevice.h>
#include <linux/mm.h>
#include <linux/module.h>
#include <linux/rmap.h>
#include <linux/uaccess.h>

#include "native_physical_usage_hook.h"
#include "native_physical_usage_mock.h"

static void mock_alloc(struct page *page)
{
#ifdef HAVE_folio_add_new_anon_rmap
	CALL_folio_add_new_anon_rmap((struct folio *)page);
#else
	CALL_page_add_new_anon_rmap(page);
#endif
}

/* Older production hooks count one base page per call. */
static void mock_alloc_large(struct page *page)
{
#ifdef HAVE_folio_remove_rmap_ptes
	mock_alloc(page);
#else
	int i;
	for (i = 0; i < 4; i++)
		mock_alloc(page + i);
#endif
}

static void mock_free(struct page *page, int count)
{
#ifdef HAVE_folio_remove_rmap_ptes
	CALL_folio_remove_rmap_ptes((struct folio *)page, page, count);
#else
	int i;
	for (i = 0; i < count; i++)
		CALL_page_remove_rmap(page + i);
#endif
}

static long run(const struct native_physical_usage_request *request)
{
	struct page *small, *large;
	if (request->scenario > NATIVE_PHYSICAL_USAGE_UNTRACKED)
		return -EINVAL;
	small = alloc_pages(GFP_KERNEL | __GFP_ZERO, 0);
	large = alloc_pages(GFP_KERNEL | __GFP_ZERO | __GFP_COMP, 2);
	if (!small || !large) {
		if (small)
			__free_pages(small, 0);
		if (large)
			__free_pages(large, 2);
		return -ENOMEM;
	}
	if (request->scenario == NATIVE_PHYSICAL_USAGE_UNTRACKED) {
		mock_free(small, 1);
	} else {
		mock_alloc_large(large);
		/* Reuse event_buf for another allocation before freeing the
		 * first. */
		mock_alloc(small);
		if (request->scenario != NATIVE_PHYSICAL_USAGE_RETAINED) {
			mock_free(small, 1);
			if (request->scenario ==
			    NATIVE_PHYSICAL_USAGE_PARTIAL) {
				mock_free(large, 1);
#ifdef HAVE_folio_remove_rmap_ptes
				mock_free(large, 1);
#else
				mock_free(large + 1, 1);
#endif
			} else {
				mock_free(large, 4);
				mock_free(large, 4); /* Untracked repeated free
							must emit nothing. */
			}
		}
	}
	/* Only the mock hooks change profiler accounting; real rmap stays
	 * untouched. */
	__free_pages(small, 0);
	__free_pages(large, 2);
	return 0;
}

static long mock_ioctl(struct file *file, unsigned int cmd, unsigned long arg)
{
	struct native_physical_usage_request request;

	if (cmd != NATIVE_PHYSICAL_USAGE_RUN)
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
    .name = "native_physical_usage_mock",
    .fops = &mock_fops,
    .mode = 0600,
};

module_misc_device(mock_device);

MODULE_LICENSE("GPL");
MODULE_DESCRIPTION("Huatuo native_physical_usage integration fixture");

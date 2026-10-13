// SPDX-License-Identifier: GPL-2.0 OR Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#ifndef HUATUO_OOM_MOCK_H
#define HUATUO_OOM_MOCK_H

#include <linux/ioctl.h>
#include <linux/types.h>

enum oom_mock_scenario {
	OOM_MOCK_SAME_TASK,
	OOM_MOCK_OTHER_TASK,
	OOM_MOCK_MEMCG,
	OOM_MOCK_OTHER_TASK_MEMCG,
	OOM_MOCK_NULL_CONTROL,
	OOM_MOCK_NULL_VICTIM,
	OOM_MOCK_POINTER_CHAIN,
};

#define OOM_MOCK_SYNTHETIC_PID 4242

/* Query only addresses unavailable through userspace process/container APIs. */
struct oom_mock_css_query {
	__u32 scenario;
	__u32 victim_pid;
	__u64 trigger_css;
	__u64 victim_css;
};

struct oom_mock_request {
	__u32 scenario;
	__u32 victim_pid;
	__u64 total_pages;
	__u64 usage_pages;
};

#define HUATUO_OOM_MOCK_GET_CSS _IOWR('H', 2, struct oom_mock_css_query)
#define HUATUO_OOM_MOCK_RUN _IOW('H', 1, struct oom_mock_request)

#endif

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

struct oom_mock_request {
	__u32 scenario;
	__u32 victim_pid;
	__u64 total_pages;
	__u64 usage_pages;
	__u32 trigger_tgid;
	__u32 victim_tgid;
	__u64 trigger_css;
	__u64 victim_css;
};

#define HUATUO_OOM_MOCK_SCENARIO _IOWR('H', 4, struct oom_mock_request)

#endif

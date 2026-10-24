// SPDX-License-Identifier: GPL-2.0 OR Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#ifndef HUATUO_RECLAIM_MOCK_H
#define HUATUO_RECLAIM_MOCK_H

#include <linux/ioctl.h>
#include <linux/types.h>

enum reclaim_mock_scenario {
	RECLAIM_MOCK_CURRENT_TASK,
};

#define RECLAIM_MOCK_DELAY_MS 20

struct reclaim_mock_request {
	__u32 scenario;
};

#define HUATUO_RECLAIM_MOCK_RUN _IOW('H', 1, struct reclaim_mock_request)

#endif

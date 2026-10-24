// SPDX-License-Identifier: GPL-2.0 OR Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#ifndef HUATUO_HUNGTASK_MOCK_H
#define HUATUO_HUNGTASK_MOCK_H

#include <linux/ioctl.h>
#include <linux/types.h>

enum hungtask_mock_scenario {
	HUNGTASK_MOCK_CURRENT_TASK,
};

struct hungtask_mock_request {
	__u32 scenario;
};

#define HUATUO_HUNGTASK_MOCK_RUN _IOW('H', 1, struct hungtask_mock_request)

#endif

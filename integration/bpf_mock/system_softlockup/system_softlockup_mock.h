// SPDX-License-Identifier: GPL-2.0 OR Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#ifndef HUATUO_SOFTLOCKUP_MOCK_H
#define HUATUO_SOFTLOCKUP_MOCK_H

#include <linux/ioctl.h>
#include <linux/types.h>

enum softlockup_mock_scenario {
	SOFTLOCKUP_MOCK_TAINT_SOFTLOCKUP = 0,
	SOFTLOCKUP_MOCK_TAINT_USER = 1,
};

struct softlockup_mock_request {
	__u32 scenario;
};

#define HUATUO_SOFTLOCKUP_MOCK_RUN _IOW('H', 1, struct softlockup_mock_request)

#endif

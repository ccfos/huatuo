// SPDX-License-Identifier: GPL-2.0 OR Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#ifndef NATIVE_PHYSICAL_USAGE_MOCK_H
#define NATIVE_PHYSICAL_USAGE_MOCK_H
#include <linux/ioctl.h>
#include <linux/types.h>
enum native_physical_usage_scenario {
	NATIVE_PHYSICAL_USAGE_RETAINED,
	NATIVE_PHYSICAL_USAGE_BALANCED,
	NATIVE_PHYSICAL_USAGE_PARTIAL,
	NATIVE_PHYSICAL_USAGE_UNTRACKED
};
struct native_physical_usage_request {
	__u32 scenario;
};
#define NATIVE_PHYSICAL_USAGE_RUN                                              \
	_IOW('H', 1, struct native_physical_usage_request)
#endif

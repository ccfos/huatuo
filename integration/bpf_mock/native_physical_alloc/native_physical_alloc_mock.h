// SPDX-License-Identifier: GPL-2.0 OR Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#ifndef NATIVE_PHYSICAL_ALLOC_MOCK_H
#define NATIVE_PHYSICAL_ALLOC_MOCK_H
#include <linux/ioctl.h>
#include <linux/types.h>
enum native_physical_alloc_scenario { NATIVE_PHYSICAL_ALLOC_MIXED };
struct native_physical_alloc_request {
	__u32 scenario;
};
#define NATIVE_PHYSICAL_ALLOC_RUN                                              \
	_IOW('H', 1, struct native_physical_alloc_request)
#endif

// SPDX-License-Identifier: GPL-2.0 OR Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#ifndef NATIVE_VIRTUAL_ALLOC_MOCK_H
#define NATIVE_VIRTUAL_ALLOC_MOCK_H
#include <linux/ioctl.h>
#include <linux/types.h>
enum native_virtual_alloc_scenario {
	NATIVE_VIRTUAL_ALLOC_MIXED,
	NATIVE_VIRTUAL_ALLOC_FILE_ONLY
};
struct native_virtual_alloc_request {
	__u32 scenario;
};
#define NATIVE_VIRTUAL_ALLOC_RUN                                               \
	_IOW('H', 1, struct native_virtual_alloc_request)
#endif

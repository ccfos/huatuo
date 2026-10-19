// SPDX-License-Identifier: GPL-2.0 OR Apache-2.0
// Copyright 2026 The HuaTuo Authors.

#ifndef MEMORY_FREE_COMPACT_MOCK_H
#define MEMORY_FREE_COMPACT_MOCK_H
#include <linux/ioctl.h>
#include <linux/types.h>
enum memory_free_compact_scenario {
	FREE_COMPACT_RECLAIM,
	FREE_COMPACT_COMPACTION,
	FREE_COMPACT_UNMATCHED_END,
};
#define FREE_COMPACT_DELAY_MS 20
struct memory_free_compact_request {
	__u32 scenario;
};
#define MEMORY_FREE_COMPACT_RUN _IOW('H', 1, struct memory_free_compact_request)
#endif

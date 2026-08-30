// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/* IO map layouts and numeric contracts are shared with generated Go types. */
#ifndef __BPF_ABI_IO_CONTROL_H__
#define __BPF_ABI_IO_CONTROL_H__

#include "bpf_abi.h"

enum io_control_wait_layout {
	IO_CONTROL_WAIT_COUNT_BITS = 26,
	IO_CONTROL_WAIT_10US_BITS = 38,
	IO_CONTROL_WAIT_UNIT_NS = 10000,
	IO_CONTROL_WAIT_COUNTER_SIZE = sizeof(u64),
};

/* Kernel CSS serial identifies a counter lifetime; userspace owns its delta. */
struct io_control_wait_key {
	u64 css;
	u64 css_serial;
	u32 major;
	u32 minor;
	u32 operation;
	u32 reserved;
};

struct io_control_status {
	u64 failure;
	u64 devices_changed;
};

enum io_control_iocost_failure {
	IOCOST_FAILURE_IDENTITY = 1,
	IOCOST_FAILURE_PENDING_COLLISION,
	IOCOST_FAILURE_PENDING_INSERT,
	IOCOST_FAILURE_PENDING_DELETE,
	IOCOST_FAILURE_AGGREGATE_INSERT,
	IOCOST_FAILURE_WAKE_FRAME,
	IOCOST_FAILURE_TIME_ROLLBACK,
	IOCOST_FAILURE_OWNER_INSERT,
	IOCOST_FAILURE_AGGREGATE_DELETE,
	IOCOST_FAILURE_OWNER_DELETE,
};

enum io_control_throtl_failure {
	THROTL_FAILURE_PENDING_COLLISION = 1,
	THROTL_FAILURE_PENDING_INSERT,
	THROTL_FAILURE_PENDING_DELETE,
	THROTL_FAILURE_OWNER,
	THROTL_FAILURE_AGGREGATE,
	THROTL_FAILURE_UNKNOWN_CALLER,
	THROTL_FAILURE_TIME_ROLLBACK,
	THROTL_FAILURE_POP_STATE,
};

BPF_ABI_EXPORT(io_control_wait_key);
BPF_ABI_EXPORT(io_control_status);
BPF_ABI_EXPORT_ENUM(io_control_wait_layout);
BPF_ABI_EXPORT_ENUM(io_control_iocost_failure);
BPF_ABI_EXPORT_ENUM(io_control_throtl_failure);

#endif /* __BPF_ABI_IO_CONTROL_H__ */

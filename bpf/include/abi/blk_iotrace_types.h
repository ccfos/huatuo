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

/* BlkIOTrace map layouts and numeric contracts also generate the Go decoder. */
#ifndef __BPF_ABI_BLK_IOTRACE_H__
#define __BPF_ABI_BLK_IOTRACE_H__

#include "bpf_abi.h"

enum blk_iotrace_layout {
	BLK_IOTRACE_BUCKETS = 17,
	IO_SIZE_BUCKETS = 6,
	BLK_IOTRACE_OPERATIONS = 2,
	BLK_IOTRACE_DISK_NAME_LEN = 32,
	BLK_IOTRACE_REQUEST_SCAN_BUDGET = 512,
	BLK_IOTRACE_CRYPTO_MODE_CAPACITY = 64,
};

enum blk_iotrace_stage {
	BLK_IOTRACE_STAGE_Q2D = 0,
	BLK_IOTRACE_STAGE_D2C,
	BLK_IOTRACE_STAGE_Q2G,
	BLK_IOTRACE_STAGE_COUNT,
};

enum blk_iotrace_counter_kind {
	BLK_IOTRACE_ROW_COUNTERS = 0,
	BLK_IOTRACE_CSS_RELEASED,
};

enum blk_iotrace_failure {
	BLK_IOTRACE_CHAIN_OVERFLOW = 1,
	BLK_IOTRACE_GROUP_INSERT_FAILED = 3,
	BLK_IOTRACE_GROUP_RELEASE_FAILED,
};

enum blk_iotrace_queue_probe_status {
	BLK_IOTRACE_PROBE_PENDING = 0,
	BLK_IOTRACE_PROBE_SKIPPED,
	BLK_IOTRACE_PROBE_SUPPORTED,
	BLK_IOTRACE_PROBE_DISABLED,
};

enum blk_iotrace_crypto_probe_status {
	BLK_IOTRACE_CRYPTO_PENDING = 0,
	BLK_IOTRACE_CRYPTO_UNUSED,
	BLK_IOTRACE_CRYPTO_INITIALIZED,
};

/* Preserve the Q2D, D2C, Q2G wire order when indexing stages on either side. */
struct blk_iotrace_counters {
	u64 buckets[BLK_IOTRACE_STAGE_COUNT][BLK_IOTRACE_BUCKETS];
	u64 queued_size[IO_SIZE_BUCKETS];
	u64 issued_size[IO_SIZE_BUCKETS];
};

struct blk_iotrace_host_counters {
	u32 major;
	u32 minor;
	struct blk_iotrace_counters counters[BLK_IOTRACE_OPERATIONS];
	u64 scsi;
};

struct blk_iotrace_container_key {
	u64 blkcg;
	u64 serial;
	u32 major;
	u32 minor;
	u32 operation;
	u32 kind;
};

struct blk_iotrace_queue_probe_key {
	u8 name[BLK_IOTRACE_DISK_NAME_LEN];
};

struct blk_iotrace_queue_probe_result {
	s64 result;
	u32 major;
	u32 minor;
	u64 disk;
};

BPF_ABI_EXPORT(blk_iotrace_counters);
BPF_ABI_EXPORT(blk_iotrace_host_counters);
BPF_ABI_EXPORT(blk_iotrace_container_key);
BPF_ABI_EXPORT(blk_iotrace_queue_probe_key);
BPF_ABI_EXPORT(blk_iotrace_queue_probe_result);
BPF_ABI_EXPORT_ENUM(blk_iotrace_layout);
BPF_ABI_EXPORT_ENUM(blk_iotrace_stage);
BPF_ABI_EXPORT_ENUM(blk_iotrace_counter_kind);
BPF_ABI_EXPORT_ENUM(blk_iotrace_failure);
BPF_ABI_EXPORT_ENUM(blk_iotrace_queue_probe_status);
BPF_ABI_EXPORT_ENUM(blk_iotrace_crypto_probe_status);

#endif /* __BPF_ABI_BLK_IOTRACE_H__ */

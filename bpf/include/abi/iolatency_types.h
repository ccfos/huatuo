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

/* IOLatency map layouts and numeric contracts also generate the Go decoder. */
#ifndef __BPF_ABI_IOLATENCY_H__
#define __BPF_ABI_IOLATENCY_H__

#include "bpf_abi.h"

enum iolatency_layout {
	IO_LATENCY_BUCKETS = 17,
	IO_SIZE_BUCKETS = 6,
	IO_LATENCY_OPERATIONS = 2,
	IO_LATENCY_DISK_NAME_LEN = 32,
	IO_LATENCY_REQUEST_SCAN_BUDGET = 512,
	IO_LATENCY_CRYPTO_MODE_CAPACITY = 64,
};

enum iolatency_stage {
	IO_LATENCY_STAGE_Q2D = 0,
	IO_LATENCY_STAGE_D2C,
	IO_LATENCY_STAGE_Q2G,
	IO_LATENCY_STAGE_COUNT,
};

enum iolatency_counter_kind {
	IO_LATENCY_ROW_COUNTERS = 0,
	IO_LATENCY_CSS_RELEASED,
};

enum iolatency_failure {
	IO_LATENCY_CHAIN_OVERFLOW = 1,
	IO_LATENCY_GROUP_INSERT_FAILED = 3,
	IO_LATENCY_GROUP_RELEASE_FAILED,
};

enum iolatency_queue_probe_status {
	IO_LATENCY_PROBE_PENDING = 0,
	IO_LATENCY_PROBE_SKIPPED,
	IO_LATENCY_PROBE_SUPPORTED,
	IO_LATENCY_PROBE_DISABLED,
};

enum iolatency_crypto_probe_status {
	IO_LATENCY_CRYPTO_PENDING = 0,
	IO_LATENCY_CRYPTO_UNUSED,
	IO_LATENCY_CRYPTO_INITIALIZED,
};

/* Preserve the Q2D, D2C, Q2G wire order when indexing stages on either side. */
struct iolatency_counters {
	u64 buckets[IO_LATENCY_STAGE_COUNT][IO_LATENCY_BUCKETS];
	u64 queued_size[IO_SIZE_BUCKETS];
	u64 issued_size[IO_SIZE_BUCKETS];
};

struct iolatency_host_counters {
	u32 major;
	u32 minor;
	struct iolatency_counters counters[IO_LATENCY_OPERATIONS];
	u64 scsi;
};

struct iolatency_container_key {
	u64 blkcg;
	u64 serial;
	u32 major;
	u32 minor;
	u32 operation;
	u32 kind;
};

struct iolatency_queue_probe_key {
	u8 name[IO_LATENCY_DISK_NAME_LEN];
};

struct iolatency_queue_probe_result {
	s64 result;
	u32 major;
	u32 minor;
	u64 disk;
};

BPF_ABI_EXPORT(iolatency_counters);
BPF_ABI_EXPORT(iolatency_host_counters);
BPF_ABI_EXPORT(iolatency_container_key);
BPF_ABI_EXPORT(iolatency_queue_probe_key);
BPF_ABI_EXPORT(iolatency_queue_probe_result);
BPF_ABI_EXPORT_ENUM(iolatency_layout);
BPF_ABI_EXPORT_ENUM(iolatency_stage);
BPF_ABI_EXPORT_ENUM(iolatency_counter_kind);
BPF_ABI_EXPORT_ENUM(iolatency_failure);
BPF_ABI_EXPORT_ENUM(iolatency_queue_probe_status);
BPF_ABI_EXPORT_ENUM(iolatency_crypto_probe_status);

#endif /* __BPF_ABI_IOLATENCY_H__ */

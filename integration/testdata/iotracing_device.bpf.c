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

#include "vmlinux.h"
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include "bpf_common.h"
#include "bpf_blkio.h"

char __license[] SEC("license") = "Dual MIT/GPL";

struct request_device_result {
	dev_t bio_observed;
	u64 count;
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, dev_t);
	__type(value, struct request_device_result);
	__uint(max_entries, 128);
} request_devices SEC(".maps");

SEC("kprobe/blk_mq_start_request")
int BPF_KPROBE(test_request_device, struct request *req)
{
	struct bio *bio = BPF_CORE_READ(req, bio);
	struct bio___5_12 *bio_new = (struct bio___5_12 *)bio;
	dev_t expected;
	struct request_device_result result = {
		.count = 1,
	};
	struct request_device_result *old;
	u32 bio_dev[2] = {};

	bio_major_minor_numbers(bio, bio_dev);
	result.bio_observed = bio_dev[0] << 20 | bio_dev[1];

	if (bpf_core_field_exists(bio_new->bi_bdev)) {
		expected = BPF_CORE_READ(bio_new, bi_bdev, bd_dev);
	} else {
		struct gendisk *disk = bio_disk(bio);
		u32 major = BPF_CORE_READ(disk, major);
		u32 minor = BPF_CORE_READ(disk, first_minor);

		if (bpf_core_field_exists(bio->bi_partno))
			minor += BPF_CORE_READ(bio, bi_partno);
		expected = major << 20 | minor;
	}

	old = bpf_map_lookup_elem(&request_devices, &expected);
	if (old)
		__sync_fetch_and_add(&old->count, 1);
	else
		bpf_map_update_elem(&request_devices, &expected, &result, COMPAT_BPF_NOEXIST);

	return 0;
}

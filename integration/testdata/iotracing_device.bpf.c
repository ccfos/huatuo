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

#include "../../bpf/iotracing.c"
#include "bpf_blkio.h"

struct request_device_result {
	dev_t observed;
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
	void *part = BPF_CORE_READ(req, part);
	dev_t expected;
	struct request_device_result result = {
		.observed = get_request_dev(req),
		.count = 1,
	};
	struct request_device_result *old;
	u32 bio_dev[2] = {};

	bio_major_minor_numbers(BPF_CORE_READ(req, bio), bio_dev);
	result.bio_observed = bio_dev[0] << 20 | bio_dev[1];

	if (bpf_core_field_exists(((struct hd_struct *)part)->partno))
		expected = BPF_CORE_READ((struct hd_struct *)part, __dev.devt);
	else
		expected = BPF_CORE_READ((struct block_device *)part, bd_dev);

	old = bpf_map_lookup_elem(&request_devices, &expected);
	if (old)
		__sync_fetch_and_add(&old->count, 1);
	else
		bpf_map_update_elem(&request_devices, &expected, &result, COMPAT_BPF_NOEXIST);

	return 0;
}

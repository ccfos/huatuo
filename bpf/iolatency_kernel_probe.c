/*
 * Copyright 2026 The HuaTuo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/*
 * Inspect initialized software encryption at startup.
 * The running kernel's enum supplies the full tfms_inited array length.
 * Byte-sized reads work with the legacy probe-read helper and preserve its
 * error result; a failed read never means that encryption is unused.
 */
#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

char __license[] SEC("license") = "Dual MIT/GPL";

const volatile __u64 io_latency_tfms_inited = 0;
const volatile __u32 io_latency_crypto_modes = 0;
struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__uint(max_entries, 1);
	__type(key, __u32);
	__type(value, __s64);
} io_latency_crypto_map SEC(".maps");

SEC("raw_tracepoint/sys_enter")
int probe_crypto_fallback(struct bpf_raw_tracepoint_args *ctx)
{
	__u32 key = 0;
	__s64 *result = bpf_map_lookup_elem(&io_latency_crypto_map, &key);
	__s64 completed = 1;
	int i;

	if (!result || *result)
		return 0;

	/* Match ioLatencyCryptoModeCapacity; Go rejects larger runtime arrays. */
#pragma unroll
	for (i = 0; i < 64; i++) {
		__u8 initialized = 0;
		int ret;

		if (i >= io_latency_crypto_modes)
			break;
		ret = (int)bpf_probe_read(&initialized, sizeof(initialized),
					 (const void *)(io_latency_tfms_inited + i));
		if (ret) {
			completed = ret;
			break;
		}
		if (initialized)
			completed = 2;
	}
	/* Publish one complete observation in a single aligned 64-bit value. */
	*result = completed;
	return 0;
}

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
 * Probe raw tracepoint argument counts at startup. Fixed-offset context
 * loads let the kernel reject slots beyond an event's argument list during
 * attach. Volatile preserves each load without dereferencing its value or
 * retaining event state. The context is read as the raw u64 argument array,
 * so probing needs no CO-RE field relocation. A default section event
 * satisfies loaders; each attachment selects the event being measured.
 */

#include "vmlinux.h"

#include <bpf/bpf_helpers.h>

char __license[] SEC("license") = "Dual MIT/GPL";

#define PROBE_ARGUMENT(slot) \
	SEC("raw_tracepoint/block_bio_queue") \
	int probe_arg##slot(struct bpf_raw_tracepoint_args *ctx) \
	{ \
		const volatile __u64 *args = (const volatile __u64 *)ctx; \
		(void)args[slot]; \
		return 0; \
	}

PROBE_ARGUMENT(0)
PROBE_ARGUMENT(1)
PROBE_ARGUMENT(2)
PROBE_ARGUMENT(3)
PROBE_ARGUMENT(4)

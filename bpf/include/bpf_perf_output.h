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

#ifndef __BPF_PERF_OUTPUT_H__
#define __BPF_PERF_OUTPUT_H__

#include <bpf/bpf_helpers.h>

#include "abi/bpf_perf_output_types.h"

// BPF_PERF_OUTPUT_IN_MAP declares the per-CPU stats map that counts events
// that bpf_perf_event_output failed to deliver. Update the counter with
// bpf_perf_event_output_counted below.
#define BPF_PERF_OUTPUT_IN_MAP(name)                                           \
	struct {                                                               \
		__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);                       \
		__uint(max_entries, 1);                                        \
		__type(key, u32);                                              \
		__type(value, struct bpf_perf_output_stats);                   \
	} bpf_perf_out_##name SEC(".maps");

// bpf_perf_event_output_counted: bpf_perf_event_output with loss accounting
//
// On failure (negative return, e.g. no reader attached for the current CPU or
// the ring buffer is full on newer kernels) increments the per-CPU lost
// counter in stats_map. The helper return value is preserved so callers can
// keep acting on success or failure.
static __always_inline long
bpf_perf_event_output_counted(void *ctx, void *perf_map, void *stats_map,
			      u64 flags, void *data, u64 size)
{
	u32 key = 0;
	long ret = bpf_perf_event_output(ctx, perf_map, flags, data, size);
	if (ret < 0) {
		struct bpf_perf_output_stats *stats =
			bpf_map_lookup_elem(stats_map, &key);
		if (stats)
			__sync_fetch_and_add(&stats->lost, 1);
	}
	return ret;
}

#endif /* __BPF_PERF_OUTPUT_H__ */

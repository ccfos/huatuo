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

/* Emit the production event ABI on a controlled syscall, without memory pressure. */
#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>
#include "bpf_cgroup.h"
#include "bpf_common.h"
#include "abi/memory_reclaim_types.h"

char __license[] SEC("license") = "Dual MIT/GPL";
volatile const u64 reclaim_duration_threshold_ns = 0;

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(u32));
} reclaim_perf_events SEC(".maps");

SEC("tracepoint/syscalls/sys_enter_getpid")
int tracepoint_reclaim_fixture(void *ctx)
{
	struct memory_reclaim_event data = {};
	const char prefix[] = "huatuo-reclaim";
	bpf_get_current_comm(data.comm, sizeof(data.comm));
#pragma unroll
	for (int i = 0; i < 14; i++) {
		if (data.comm[i] != prefix[i])
			return 0;
	}
	if (data.comm[14] != '0' && data.comm[14] != '1' && data.comm[14] != '2')
		return 0;
	data.reclaim_duration_ns = 1000000001;
	if (data.reclaim_duration_ns <= reclaim_duration_threshold_ns)
		return 0;
	data.cpu_css_addr = data.comm[14] == '1' ? 0x1234 : 0;
	if (data.comm[14] == '2')
		data.cpu_css_addr = current_task_cpu_css_addr();
	u64 id = bpf_get_current_pid_tgid();
	data.tgid = id >> 32;
	data.tid = (u32)id;
	bpf_perf_event_output(ctx, &reclaim_perf_events,
			     COMPAT_BPF_F_CURRENT_CPU, &data, sizeof(data));
	return 0;
}

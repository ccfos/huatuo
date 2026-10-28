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


#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>
#include "bpf_cgroup.h"
#include "bpf_common.h"
#include "abi/hungtask_types.h"

char __license[] SEC("license") = "Dual MIT/GPL";

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(u32));
} hungtask_perf_events SEC(".maps");

static __always_inline int emit_hungtask(void *ctx)
{
	struct hungtask_event event = {};
	const char expected[] = "huatuo-hung";

	bpf_get_current_comm(event.comm, sizeof(event.comm));
#pragma unroll
	for (int i = 0; i < sizeof(expected); i++) {
		if (event.comm[i] != expected[i])
			return 0;
	}
	event.tid = (u32)bpf_get_current_pid_tgid();
#ifdef CONTAINER_FIXTURE
	event.cpu_css_addr = current_task_cpu_css_addr();
#endif
	bpf_perf_event_output(ctx, &hungtask_perf_events,
			     COMPAT_BPF_F_CURRENT_CPU, &event, sizeof(event));
	return 0;
}

#ifdef FORCE_FALLBACK
SEC("raw_tracepoint/huatuo_fixture_missing")
#else
SEC("raw_tracepoint/sys_enter")
#endif
int raw_sched_process_hang(struct bpf_raw_tracepoint_args *ctx)
{
	/* sys_enter exposes the syscall number, unlike the per-syscall fallback. */
#if defined(__TARGET_ARCH_x86)
	if (ctx->args[1] != 39)
		return 0;
#elif defined(__TARGET_ARCH_arm64)
	if (ctx->args[1] != 172)
		return 0;
#else
#error Unsupported fixture architecture
#endif
	return emit_hungtask(ctx);
}

SEC("tracepoint/syscalls/sys_enter_getpid")
int tracepoint_sched_process_hang(void *ctx)
{
	return emit_hungtask(ctx);
}

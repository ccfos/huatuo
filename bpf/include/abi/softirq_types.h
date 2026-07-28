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

#ifndef __BPF_ABI_SOFTIRQ_H__
#define __BPF_ABI_SOFTIRQ_H__

#include "bpf_abi.h"

struct softirq_event {
	u64 stack[PERF_MAX_STACK_DEPTH];
	s64 stack_size;
	u64 now;
	u64 stall_time;
	u8 comm[COMPAT_TASK_COMM_LEN];
	u32 pid;
	u32 cpu;
};

BPF_ABI_EXPORT(softirq_event);

#endif /* __BPF_ABI_SOFTIRQ_H__ */

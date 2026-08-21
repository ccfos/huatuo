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

#ifndef __BPF_ABI_IOTRACING_H__
#define __BPF_ABI_IOTRACING_H__

#include "bpf_abi.h"

struct iotracing_schedule_delay_event {
	u64 stack[PERF_MIN_STACK_DEPTH];
	u64 start_ns;
	u64 duration_ns;
	s32 stack_size;
	u32 tgid;
	u32 tid;
	u32 cpu;
	u8 comm[COMPAT_TASK_COMM_LEN];
};

BPF_ABI_EXPORT(iotracing_schedule_delay_event);

#endif /* __BPF_ABI_IOTRACING_H__ */

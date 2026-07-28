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

#ifndef __BPF_ABI_PROFILER_H__
#define __BPF_ABI_PROFILER_H__

#include "bpf_abi.h"

struct profiler_event_base {
	u64 pid_tgid;
	u8 comm[COMPAT_TASK_COMM_LEN];
	s32 kernstack;
	s32 userstack;
	s64 value;
};

struct profiler_cpu_event {
	struct profiler_event_base base;
	u64 timestamp;
	u32 cpu;
	u32 pad0;
};

BPF_ABI_EXPORT(profiler_event_base);
BPF_ABI_EXPORT(profiler_cpu_event);

#endif /* __BPF_ABI_PROFILER_H__ */

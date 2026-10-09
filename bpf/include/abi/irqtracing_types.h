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

#ifndef __BPF_ABI_IRQTRACING_H__
#define __BPF_ABI_IRQTRACING_H__

#include "bpf_abi.h"

enum irqtracing_target_cpu {
	IRQTRACING_TARGET_ALL_CPUS = -1,
};

enum irqtracing_stream {
	IRQTRACING_STREAM_SOURCE = 0,
	IRQTRACING_STREAM_VICTIM,
	IRQTRACING_STREAM_MAX,
};

enum irqtracing_stack_id {
	IRQTRACING_STACK_ID_NONE = 0xffffffffU,
};

struct irqtracing_stack_key {
	u32 ustack_id;
	u32 kstack_id;
	u32 pid;
	u32 vec;
	u8 comm[COMPAT_TASK_COMM_LEN];
};

BPF_ABI_EXPORT(irqtracing_stack_key);
BPF_ABI_EXPORT_ENUM(irqtracing_target_cpu);
BPF_ABI_EXPORT_ENUM(irqtracing_stream);
BPF_ABI_EXPORT_ENUM(irqtracing_stack_id);

#endif /* __BPF_ABI_IRQTRACING_H__ */

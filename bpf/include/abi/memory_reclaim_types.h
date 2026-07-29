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

#ifndef __BPF_ABI_MEMORY_RECLAIM_H__
#define __BPF_ABI_MEMORY_RECLAIM_H__

#include "bpf_abi.h"

struct memory_reclaim_event {
	u8 comm[COMPAT_TASK_COMM_LEN];
	u64 delta_time;
	u64 css;
	u64 pid;
};

BPF_ABI_EXPORT(memory_reclaim_event);

#endif /* __BPF_ABI_MEMORY_RECLAIM_H__ */

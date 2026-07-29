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

#ifndef __BPF_ABI_RAS_H__
#define __BPF_ABI_RAS_H__

#include "bpf_abi.h"

#define RAS_EVENT_INFO_SIZE 512

struct ras_event {
	u32 type;
	u32 pad0;
	u64 timestamp;
	u8 info[RAS_EVENT_INFO_SIZE];
};

struct ras_thr_info {
	u32 vector;
	u32 cpu;
};

BPF_ABI_EXPORT(ras_event);
BPF_ABI_EXPORT(ras_thr_info);

#endif /* __BPF_ABI_RAS_H__ */

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

/* IO health perf events use this layout for BPF output and generated Go types. */
#ifndef __BPF_ABI_IO_HEALTH_H__
#define __BPF_ABI_IO_HEALTH_H__

#include "bpf_abi.h"

enum io_health_event_type {
	HEALTH_EVENT_BLOCK_ERROR = 1,
};

struct io_health_event {
	u64 sector;
	u32 dev;
	s32 status;
	u8 type;
	u8 operation;
	u8 rwbs[2];
	u8 pad[4];
};

BPF_ABI_EXPORT(io_health_event);
BPF_ABI_EXPORT_ENUM(io_health_event_type);

#endif /* __BPF_ABI_IO_HEALTH_H__ */

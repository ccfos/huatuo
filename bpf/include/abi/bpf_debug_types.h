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

#ifndef __BPF_ABI_BPF_DEBUG_H__
#define __BPF_ABI_BPF_DEBUG_H__

#include "bpf_abi.h"

#define BPF_DEBUG_MSG_LEN  64
#define BPF_DEBUG_FILE_LEN 64

struct bpf_debug_event {
	u8 file_name[BPF_DEBUG_FILE_LEN];
	u32 file_line;
	u32 pad0;
	u8 msg[BPF_DEBUG_MSG_LEN];
	u64 args[4];
	u64 timestamp;
};

BPF_ABI_EXPORT(bpf_debug_event);

#endif /* __BPF_ABI_BPF_DEBUG_H__ */

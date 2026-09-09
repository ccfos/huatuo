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

#ifndef __BPF_ABI_SCHED_BLAME_H__
#define __BPF_ABI_SCHED_BLAME_H__

#include "bpf_abi.h"

#ifndef EXTRA_BITMAP_U64_COUNT
#define EXTRA_BITMAP_U64_COUNT 1
#endif

#define SCHED_BLAME_MAX_SLICES_PER_BATCH 128

struct sched_blame_identity_event {
	u16 magic;
	u16 css_id;
	u8 padding[4];
	u64 cgid;
};

struct sched_blame_throttle_event {
	u16 magic;
	u16 dense_id;
	u32 duration_ns;
};

struct sched_blame_packed_slice {
	u64 packed_base;
#if EXTRA_BITMAP_U64_COUNT > 0
	u64 bitmap_extra[EXTRA_BITMAP_U64_COUNT];
#endif
};

struct sched_blame_slice_batch_event {
	u16 magic;
	u16 extra_bitmap_u64_count;
	u32 count;
	struct sched_blame_packed_slice
		packed_slices[SCHED_BLAME_MAX_SLICES_PER_BATCH];
};

BPF_ABI_EXPORT(sched_blame_identity_event);
BPF_ABI_EXPORT(sched_blame_throttle_event);
BPF_ABI_EXPORT(sched_blame_packed_slice);
BPF_ABI_EXPORT(sched_blame_slice_batch_event);

#endif /* __BPF_ABI_SCHED_BLAME_H__ */

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

/* IO probes and GC share one cache. Keep its layout and non-blocking gate
 * together so reactivation and reclamation use the same ownership protocol.
 */
#ifndef HUATUO_BPF_IOLATENCY_CACHE_H
#define HUATUO_BPF_IOLATENCY_CACHE_H

#include "vmlinux.h"
#include <bpf/bpf_helpers.h>

#include "bpf_common.h"

#define IO_LATENCY_BIO_STATES 10240
#define IO_LATENCY_ENOENT 2
#define IO_LATENCY_EBUSY 16

struct bio_latency_state {
	/* queue_ns=0 publishes inactivity after C finishes all value accesses. */
	u64 queue_ns;
	u64 blkcg;
	u32 major;
	u32 minor;
	u64 inactive_ns;
	/* Activation and GC own the guard; C only publishes inactivity. */
	u32 guard;
	u32 deleted;
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct bio_latency_state);
	__uint(max_entries, IO_LATENCY_BIO_STATES);
	/* Startup admits allocators that defer node reuse until C leaves RCU. */
	__uint(map_flags, COMPAT_BPF_F_NO_PREALLOC);
} bio_latency_map SEC(".maps");

/*
 * 4.18 supports non-fetching XADD. Increment, then read the actual counter;
 * a value of one owns the entry. Contenders undo only their increment and
 * return without spinning, including an interrupt of an entry owner.
 * On the target x86_64, locked add orders accesses around this gate.
 */
static __always_inline u32 *io_latency_try_gate(struct bio_latency_state *state)
{
	u32 *gate = &state->guard;

	__sync_fetch_and_add(gate, 1);
	if (*(volatile u32 *)gate == 1)
		return gate;
	__sync_fetch_and_add(gate, (u32)-1);
	return NULL;
}

static __always_inline void io_latency_release_gate(u32 *gate)
{
	if (gate)
		__sync_fetch_and_add(gate, (u32)-1);
}

#endif

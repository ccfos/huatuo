// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/* Recheck userspace GC candidates under the cache's activation gate. Load
 * this maintenance program locally and share the IO probes' existing map.
 */
#include "vmlinux.h"

#include <bpf/bpf_helpers.h>

#include "bpf_iolatency_cache.h"

char __license[] SEC("license") = "Dual MIT/GPL";

struct bio_gc_job {
	u64 bio;
	u64 observed_inactive_ns;
	u64 cutoff_ns;
};

/* Candidate selection is advisory; both inactivity and age are rechecked
 * while Q/A cannot activate the cache node. Newer completions survive a
 * userspace scan that observed the same address during an older idle period.
 */
static __always_inline int io_latency_reclaim_bio(
	u64 key, u64 observed_inactive_ns, u64 cutoff_ns)
{
	struct bio_latency_state *state;
	u32 *gate;
	int ret = 0;

	state = bpf_map_lookup_elem(&bio_latency_map, &key);
	if (!state)
		return 0;
	gate = io_latency_try_gate(state);
	if (!gate)
		return -IO_LATENCY_EBUSY;
	if (!state->deleted && !state->queue_ns &&
	    state->inactive_ns == observed_inactive_ns &&
	    state->inactive_ns <= cutoff_ns) {
		/* RCU keeps this node readable after unlink. A Q/A that already
		 * looked it up observes this marker and uses fallback storage.
		 */
		*(volatile u32 *)&state->deleted = 1;
		asm volatile("" ::: "memory");
		ret = bpf_map_delete_elem(&bio_latency_map, &key);
		if (!ret)
			ret = 1;
		else if (ret == -IO_LATENCY_ENOENT)
			ret = 0;
		else
			*(volatile u32 *)&state->deleted = 0;
	}
	io_latency_release_gate(gate);
	return ret;
}

/* The session loads this program separately and shares the cache.
 * It submits candidates with BPF_PROG_TEST_RUN, without a network attachment.
 */
SEC("classifier/iolatency_gc")
int bio_gc_run(struct __sk_buff *ctx)
{
	struct bio_gc_job job = {};
	int ret;

	/* Test-run carries native-endian fields after Ethernet/IPv4 headers. */
	ret = bpf_skb_load_bytes(ctx, sizeof(struct ethhdr) + sizeof(struct iphdr),
				 &job, sizeof(job));
	if (ret)
		return ret;
	return io_latency_reclaim_bio(job.bio, job.observed_inactive_ns,
				      job.cutoff_ns);
}

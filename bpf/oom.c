/*
 * Copyright 2026 The HuaTuo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_ratelimit.h"
#include "abi/oom_types.h"

char __license[] SEC("license") = "Dual MIT/GPL";

BPF_RATELIMIT_IN_MAP(rate, 1, COMPAT_CPU_NUM * 10000, 0);

/*
 * Linux 4.18 lacks the kernel-specific probe-read helpers used by CO-RE.
 * Use the legacy probe_read helpers (IDs 4 and 45), which support these
 * kernel-memory reads and are available on supported older kernels.
 */
static long (*compat_bpf_probe_read)(void *dst, __u32 size,
				     const void *unsafe_ptr) = (void *)4; /* BPF_FUNC_probe_read */
static long (*compat_bpf_probe_read_str)(void *dst, __u32 size,
					 const void *unsafe_ptr) = (void *)45; /* BPF_FUNC_probe_read_str */

#define bpf_probe_read_kernel compat_bpf_probe_read
#define bpf_probe_read_kernel_str compat_bpf_probe_read_str

/*
 * Upstream mm_struct.rss_stat has two layouts:
 *
 * Linux 6.1 and earlier:
 *   struct mm_rss_stat rss_stat;
 *   rss_stat.count[MM_*] is atomic_long_t.
 * Linux 6.2 and later (commit f1a7941243c1):
 *   struct percpu_counter rss_stat[NR_MM_COUNTERS];
 *   rss_stat[MM_*].count is the global s64 counter.
 *
 * CO-RE checks the actual field layout, including vendor backports. The ___
 * suffix makes this a CO-RE flavor of mm_struct.
 */
struct mm_struct___percpu_rss {
	struct percpu_counter rss_stat[NR_MM_COUNTERS];
} __attribute__((preserve_access_index));

/*
 * member must be a compile-time MM_* constant so both CO-RE array accessors
 * can be relocated and the unavailable layout can be pruned by the verifier.
 */
#define READ_MM_RSS_PAGES(mm, member) ({                                \
	u64 __pages = 0;                                                 \
	struct mm_struct *__mm = (mm);                                  \
	if (bpf_core_field_exists(__mm->rss_stat.count[member])) {       \
		long __legacy = 0;                                       \
		bpf_core_read(&__legacy, sizeof(__legacy),                \
			      &__mm->rss_stat.count[member]);             \
		if (__legacy > 0)                                        \
			__pages = (u64)__legacy;                          \
	} else {                                                         \
		struct mm_struct___percpu_rss *__new = (void *)__mm;       \
		if (bpf_core_field_exists(__new->rss_stat[member].count)) {\
			s64 __percpu = 0;                                 \
			bpf_core_read(&__percpu, sizeof(__percpu),         \
				      &__new->rss_stat[member].count);    \
			if (__percpu > 0)                                  \
				__pages = (u64)__percpu;                  \
		}                                                        \
	}                                                                \
	__pages;                                                         \
})

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} oom_perf_events SEC(".maps");

SEC("kprobe/oom_kill_process")
int BPF_KPROBE(oom_kill_process, struct oom_control *oc, const char *message)
{
	struct oom_event info = {};
	struct task_struct *trigger_task, *victim_task;
	struct mm_struct *victim_mm;

	if (bpf_ratelimited_in_map(ctx, rate))
		return 0;

	if (!oc)
		return 0;

	trigger_task	 = (struct task_struct *)bpf_get_current_task();
	victim_task	 = BPF_CORE_READ(oc, chosen);

	info.trigger_pid = BPF_CORE_READ(trigger_task, pid);
	info.victim_pid	 = BPF_CORE_READ(victim_task, pid);
	BPF_CORE_READ_STR_INTO(&info.trigger_comm, trigger_task, comm);
	BPF_CORE_READ_STR_INTO(&info.victim_comm, victim_task, comm);

	victim_mm = BPF_CORE_READ(victim_task, mm);
	if (victim_mm) {
		info.victim_rss_anon_pages =
		    READ_MM_RSS_PAGES(victim_mm, MM_ANONPAGES);
		info.victim_rss_file_pages =
		    READ_MM_RSS_PAGES(victim_mm, MM_FILEPAGES);
		info.victim_rss_shmem_pages =
		    READ_MM_RSS_PAGES(victim_mm, MM_SHMEMPAGES);
		info.victim_total_vm_pages =
		    BPF_CORE_READ(victim_mm, total_vm);
	}

	info.victim_memcg_css =
	    (u64)BPF_CORE_READ(victim_task, cgroups, subsys[memory_cgrp_id]);
	info.trigger_memcg_css =
	    (u64)BPF_CORE_READ(trigger_task, cgroups, subsys[memory_cgrp_id]);

	info.mem_limit_pages = BPF_CORE_READ(oc, totalpages);
	struct mem_cgroup *memcg = BPF_CORE_READ(oc, memcg);
	if (memcg) {
		info.mem_usage_pages =
		    (u64)BPF_CORE_READ(memcg, memory.usage.counter);
	}

	bpf_perf_event_output(ctx, &oom_perf_events, COMPAT_BPF_F_CURRENT_CPU,
			      &info, sizeof(info));
	return 0;
}

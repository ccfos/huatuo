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

/*
 * Maps an OOM victim TGID to its correlation timestamp.
 */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(u64));
	__uint(max_entries, 256);
} oom_victims SEC(".maps");

/*
 * A direct .bss read avoids a map lookup on every ordinary process exit.
 */
static volatile u32 oom_victim_count;

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} oom_exit_perf_events SEC(".maps");

/*
 * A per-CPU scratch value keeps the exit event off the 512-byte BPF stack.
 */
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(struct oom_exit_event));
	__uint(max_entries, 1);
} oom_exit_event_buf SEC(".maps");

/*
 * Copy a bounded argv or environment region with the legacy probe_read
 * helper supported by CentOS 8.2.
 */
static __always_inline u16
capture_user_region(u8 *dst, u32 capacity, unsigned long start,
		    unsigned long end, u8 *flags)
{
	u64 length;

	if (!start || end <= start)
		return 0;

	length = end - start;
	if (length > capacity) {
		length = capacity;
		*flags |= OOM_CAPTURE_TRUNC;
	}
	if (compat_bpf_probe_read(dst, (u32)length, (void *)start) != 0)
		return 0;

	return (u16)length;
}

/*
 * Go places this buildinfo magic at mm->start_data for normal internal-link
 * executables. Check only that fixed address; do not scan victim memory.
 */
#define GO_BUILDINFO_MAGIC "\xff Go buildinf:"
#define GO_BUILDINFO_MAGIC_LEN 14

static __always_inline u8
has_go_buildinfo_at_start_data(unsigned long start_data)
{
	u8 actual[GO_BUILDINFO_MAGIC_LEN];
	u8 expected[] = GO_BUILDINFO_MAGIC;
	int i;

	if (!start_data ||
	    compat_bpf_probe_read(actual, sizeof(actual), (void *)start_data) != 0)
		return 0;

#pragma unroll
	for (i = 0; i < GO_BUILDINFO_MAGIC_LEN; i++) {
		if (actual[i] != expected[i])
			return 0;
	}
	return 1;
}

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
	info.victim_pid	 = BPF_CORE_READ(victim_task, tgid);
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

	/*
	 * NOEXIST preserves the first registration for a TGID. A zero timestamp
	 * tells userspace that registration failed and it must not wait for exit.
	 */
	info.timestamp = bpf_ktime_get_ns();
	if (bpf_map_update_elem(&oom_victims, &info.victim_pid,
				&info.timestamp, COMPAT_BPF_NOEXIST) == 0) {
		__sync_fetch_and_add(&oom_victim_count, 1);
	} else {
		info.timestamp = 0;
	}

	bpf_perf_event_output(ctx, &oom_perf_events, COMPAT_BPF_F_CURRENT_CPU,
			      &info, sizeof(info));
	return 0;
}

/*
 * Ordinary exits only read the pending counter. An OOM victim additionally
 * looks up and removes its key, captures argv and environ before mm teardown,
 * then emits a separately correlated event.
 */
SEC("kprobe/do_exit")
int BPF_KPROBE(do_exit_trace, long code)
{
	u32 zero = 0;
	u32 victim_tgid;
	u64 event_ts;
	u64 *victim_ts;
	struct oom_exit_event *exit_event;
	struct task_struct *victim_task;
	struct mm_struct *victim_mm;
	unsigned long arg_start, arg_end, env_start, env_end, start_data;

	/*
	 * Most exits stop at this global-data check. The hash lookup is only paid
	 * while at least one OOM victim is pending.
	 */
	if (oom_victim_count == 0)
		return 0;

	victim_tgid = (u32)(bpf_get_current_pid_tgid() >> 32);
	victim_ts =
	    bpf_map_lookup_elem(&oom_victims, &victim_tgid);
	if (!victim_ts)
		return 0;

	event_ts = *victim_ts;
	/*
	 * Consume the registration before capture so a later failure cannot make
	 * this victim run the expensive path again.
	 */
	if (bpf_map_delete_elem(&oom_victims, &victim_tgid) != 0)
		return 0;
	__sync_fetch_and_add(&oom_victim_count, -1);

	exit_event = bpf_map_lookup_elem(&oom_exit_event_buf, &zero);
	if (!exit_event)
		return 0;

	exit_event->timestamp = event_ts;
	exit_event->victim_tgid = victim_tgid;
	exit_event->cmdline_flags = 0;
	exit_event->environ_flags = 0;

	victim_task = (struct task_struct *)bpf_get_current_task();
	victim_mm = BPF_CORE_READ(victim_task, mm);
	if (!victim_mm)
		return 0;

	arg_start = BPF_CORE_READ(victim_mm, arg_start);
	arg_end = BPF_CORE_READ(victim_mm, arg_end);
	env_start = BPF_CORE_READ(victim_mm, env_start);
	env_end = BPF_CORE_READ(victim_mm, env_end);
	start_data = BPF_CORE_READ(victim_mm, start_data);
	exit_event->go_build_info =
	    has_go_buildinfo_at_start_data(start_data);

	exit_event->cmdline_len = capture_user_region(
	    exit_event->victim_cmdline, sizeof(exit_event->victim_cmdline),
	    arg_start, arg_end, &exit_event->cmdline_flags);
	exit_event->environ_len = capture_user_region(
	    exit_event->victim_environ, sizeof(exit_event->victim_environ),
	    env_start, env_end, &exit_event->environ_flags);

	bpf_perf_event_output(ctx, &oom_exit_perf_events,
			      COMPAT_BPF_F_CURRENT_CPU, exit_event,
			      sizeof(*exit_event));
	return 0;
}

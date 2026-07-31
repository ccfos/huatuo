#ifndef __BPF_LOCK_PROFILER_H__
#define __BPF_LOCK_PROFILER_H__

#include "bpf_profiler.h"

enum profiler_lock_access {
	PROFILER_LOCK_ACCESS_UNKNOWN = 0,
	PROFILER_LOCK_ACCESS_READ = 1,
	PROFILER_LOCK_ACCESS_WRITE = 2,
};

static volatile const u64 lock_wait_threshold_ns = 1000;

struct lock_wait_start_t {
	u64 started_ns;
	u64 lock;
	u32 nested;
	u8 access;
	u8 pad[3];
};

struct lock_stat_key_t {
	u64 pid_tgid;
	char comm[COMPAT_TASK_COMM_LEN];
	u64 lock;
	int kernstack;
	int userstack;
	u8 access;
	u8 pad[7];
};

struct lock_stat_value_t {
	u64 wait_ns;
	u64 contended;
};

#ifdef HUATUO_LOCK_PROFILER_SPIN
struct spin_wait_start_t {
	u64 pid_tgid;
	u64 started_ns;
	u64 lock;
	u32 nested;
	u8 active;
	u8 pad[3];
};

/*
 * A non-RT spinning task cannot migrate. A per-CPU slot avoids a global hash
 * update on this hot path. Nested begin events preserve the active outer wait.
 */
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(max_entries, 1);
	__type(key, u32);
	__type(value, struct spin_wait_start_t);
} spin_wait_starts SEC(".maps");
#else
/*
 * A blocked task can migrate. Per-CPU state would lose the end event after
 * migration, so mutex and rwlock waits use bounded task-keyed LRU storage.
 */
struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__uint(max_entries, 65536);
	__type(key, u64);
	__type(value, struct lock_wait_start_t);
} lock_wait_starts SEC(".maps");
#endif

/*
 * Reuse the profiler state and A/B stack maps. The perf output maps created by
 * this macro remain unused: lock samples are aggregated in the HASH maps below
 * instead of emitting one perf event for every contention.
 */
DEFINE_PROFILER_MAPS(struct profiler_event_base);

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 32768);
	__type(key, struct lock_stat_key_t);
	__type(value, struct lock_stat_value_t);
} lock_stats_a SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 32768);
	__type(key, struct lock_stat_key_t);
	__type(value, struct lock_stat_value_t);
} lock_stats_b SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(max_entries, 1);
	__type(key, u32);
	__type(value, u64);
} lock_active_writers_a SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(max_entries, 1);
	__type(key, u32);
	__type(value, u64);
} lock_active_writers_b SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(max_entries, 1);
	__type(key, u32);
	__type(value, u64);
} lock_dropped_stats SEC(".maps");

static __always_inline bool lock_matches_target(u64 pid_tgid)
{
	u64 cpu_css = 0;

	if (profiler_filter_css != 0)
		cpu_css = current_task_cpu_css_addr();
	return profiler_should_trace(pid_tgid, cpu_css);
}

static __always_inline int aggregate_lock_wait(void *ctx, u64 pid_tgid,
						u64 lock, u8 access,
						u64 wait_ns)
{
	u64 *transfer_count_ptr;
	u64 *sample_count_ptrs[2];
	u64 *active_writers;
	void *select_profiler_stack_map;
	void *select_lock_stats;
	void *select_active_writers;
	u32 zero_idx = 0;

	if (wait_ns < lock_wait_threshold_ns)
		return 0;
	if (!profiler_init_state(&profiler_state_map, &transfer_count_ptr,
				 sample_count_ptrs))
		return 0;

	u64 transfer_count = *transfer_count_ptr;
	if ((transfer_count & 1ULL) == 0) {
		select_profiler_stack_map = (void *)&stack_map_a;
		select_lock_stats = (void *)&lock_stats_a;
		select_active_writers = (void *)&lock_active_writers_a;
	} else {
		select_profiler_stack_map = (void *)&stack_map_b;
		select_lock_stats = (void *)&lock_stats_b;
		select_active_writers = (void *)&lock_active_writers_b;
	}

	/*
	 * Per-CPU writer counts let userspace drain a frozen map without relying
	 * on a grace period or bouncing a shared cache line on every contention.
	 */
	active_writers =
		bpf_map_lookup_elem(select_active_writers, &zero_idx);
	if (!active_writers)
		return 0;
	__sync_fetch_and_add(active_writers, 1);
	if (*transfer_count_ptr != transfer_count)
		goto release_writer;

	struct lock_stat_key_t key = {
		.pid_tgid = pid_tgid,
		.lock = lock,
		.kernstack = -1,
		.userstack = -1,
		.access = access,
	};
	bpf_get_current_comm(&key.comm, sizeof(key.comm));
	key.userstack = bpf_get_stackid(ctx, select_profiler_stack_map,
				       USER_STACKID_FLAGS);
	key.kernstack = bpf_get_stackid(ctx, select_profiler_stack_map,
				       KERN_STACKID_FLAGS);
	if (key.userstack < 0 && key.kernstack < 0)
		goto release_writer;

	struct lock_stat_value_t zero = {};
	bpf_map_update_elem(select_lock_stats, &key, &zero,
			    COMPAT_BPF_NOEXIST);
	struct lock_stat_value_t *stat =
		bpf_map_lookup_elem(select_lock_stats, &key);
	if (!stat) {
		u64 *dropped =
			bpf_map_lookup_elem(&lock_dropped_stats, &zero_idx);
		if (dropped)
			__sync_fetch_and_add(dropped, 1);
		goto release_writer;
	}

	__sync_fetch_and_add(&stat->wait_ns, wait_ns);
	__sync_fetch_and_add(&stat->contended, 1);

release_writer:
	__sync_fetch_and_sub(active_writers, 1);
	return 0;
}

#ifndef HUATUO_LOCK_PROFILER_SPIN
static __always_inline int lock_wait_begin(u64 pid_tgid, u64 lock, u8 access)
{
	if (!lock_matches_target(pid_tgid))
		return 0;

	/* Preserve the outer wait when contention is nested in the same task. */
	struct lock_wait_start_t *existing =
		bpf_map_lookup_elem(&lock_wait_starts, &pid_tgid);
	if (existing) {
		existing->nested++;
		return 0;
	}

	struct lock_wait_start_t start = {
		.started_ns = bpf_ktime_get_ns(),
		.lock = lock,
		.access = access,
	};
	bpf_map_update_elem(&lock_wait_starts, &pid_tgid, &start,
			    COMPAT_BPF_NOEXIST);
	return 0;
}

static __always_inline int lock_wait_begin_kprobe(u64 pid_tgid, u64 lock,
						  u8 access)
{
	if (!lock_matches_target(pid_tgid))
		return 0;

	struct lock_wait_start_t start = {
		.started_ns = bpf_ktime_get_ns(),
		.lock = lock,
		.access = access,
	};
	/*
	 * A return-probe instance can be missed when maxactive is exhausted.
	 * Replacing stale state bounds the loss to the missed wait.
	 */
	bpf_map_update_elem(&lock_wait_starts, &pid_tgid, &start,
			    COMPAT_BPF_ANY);
	return 0;
}

static __always_inline int lock_wait_end(void *ctx, u64 pid_tgid,
					 u64 lock, bool success)
{
	if (!lock_matches_target(pid_tgid))
		return 0;

	struct lock_wait_start_t *found =
		bpf_map_lookup_elem(&lock_wait_starts, &pid_tgid);
	if (!found)
		return 0;
	if (found->nested > 0) {
		found->nested--;
		return 0;
	}
	if (lock != 0 && found->lock != lock)
		return 0;

	struct lock_wait_start_t start = *found;
	bpf_map_delete_elem(&lock_wait_starts, &pid_tgid);
	if (!success)
		return 0;

	return aggregate_lock_wait(ctx, pid_tgid, start.lock, start.access,
				   bpf_ktime_get_ns() - start.started_ns);
}
#else
static __always_inline int spin_wait_begin(u64 lock)
{
	u64 pid_tgid = bpf_get_current_pid_tgid();

	if (!lock_matches_target(pid_tgid))
		return 0;

	u32 idx = 0;
	struct spin_wait_start_t *start =
		bpf_map_lookup_elem(&spin_wait_starts, &idx);
	if (!start)
		return 0;
	if (start->active) {
		start->nested++;
		return 0;
	}

	start->pid_tgid = pid_tgid;
	start->started_ns = bpf_ktime_get_ns();
	start->lock = lock;
	start->nested = 0;
	start->active = 1;
	return 0;
}

static __always_inline int spin_wait_end(void *ctx, u64 lock, bool success)
{
	u64 current_pid_tgid = bpf_get_current_pid_tgid();

	if (!lock_matches_target(current_pid_tgid))
		return 0;

	u32 idx = 0;
	struct spin_wait_start_t *start =
		bpf_map_lookup_elem(&spin_wait_starts, &idx);
	if (!start || !start->active)
		return 0;
	if (start->nested > 0) {
		start->nested--;
		return 0;
	}
	if (start->lock != lock)
		return 0;

	u64 pid_tgid = start->pid_tgid;
	u64 started_ns = start->started_ns;
	start->active = 0;
	if (!success)
		return 0;

	return aggregate_lock_wait(ctx, pid_tgid, lock,
				   PROFILER_LOCK_ACCESS_UNKNOWN,
				   bpf_ktime_get_ns() - started_ns);
}
#endif

struct lock_contention_begin_ctx {
	u64 common;
	u64 lock_addr;
	u32 flags;
};

struct lock_contention_end_ctx {
	u64 common;
	u64 lock_addr;
	s32 ret;
};

#endif /* __BPF_LOCK_PROFILER_H__ */

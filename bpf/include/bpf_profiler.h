#ifndef __BPF_PROFILER_H__
#define __BPF_PROFILER_H__

#include "bpf_common.h"
#include "bpf_cgroup.h"

/* Stack trace map default entries */
#define STACK_MAP_ENTRIES 65536

/* Stack ID flags for bpf_get_stackid */
#define KERN_STACKID_FLAGS (0)
#define USER_STACKID_FLAGS (0 | COMPAT_BPF_F_USER_STACK)

/* Profiler event base structure */
struct profiler_event_base_t {
	u64 pid_tgid;  /* tgid in upper 32 bits, pid in lower 32 bits */
	char comm[COMPAT_TASK_COMM_LEN];
	int kernstack;
	int userstack;
	s64 value;  /* CPU: sample count (1), Memory: page/byte delta */
};

/* State management indices */
typedef enum {
	PROFILER_STATE_TRANSFER_CNT_IDX = 0,
	PROFILER_STATE_SAMPLE_CNT_A_IDX,
	PROFILER_STATE_SAMPLE_CNT_B_IDX,
	PROFILER_STATE_CNT
} profiler_state_idx;

/*
 * Filter configuration injected via RewriteConstants at load time.
 * Naming follows skb_filter convention for consistency.
 */
static volatile const u64 profiler_filter_css = 0;
static volatile const u32 profiler_filter_pid = 0;
static volatile const bool profiler_filter_threads = false;
static volatile const u8 profiler_sampling_prob = 100;
static volatile const u64 profiler_idle_class_addr = 0;
static volatile const bool profiler_follow_forks = false;
static volatile const u32 profiler_fork_max_pids = 4096;
static volatile const u32 profiler_fork_rate = 1000;
static volatile const u32 profiler_fork_burst = 2000;

/*
 * Fork tracking deliberately lives in the common profiler object so CPU and
 * all native memory modes have identical process-lifecycle semantics.  The
 * root PID remains a rewritten constant.  Only descendants consume map
 * entries, which means the root can exit without stopping collection for its
 * surviving children.
 */
struct profiler_fork_pid_t {
	u32 root_pid;
	u32 parent_pid;
	u32 generation;
	u32 flags;
	u64 born_ns;
};

#define PROFILER_FORK_F_THREAD (1U << 0)

struct profiler_fork_stats_t {
	u64 active;
	u64 accepted;
	u64 duplicate;
	u64 update_failures;
	u64 exited;
	u64 rejected_limit;
	u64 rejected_rate;
	u64 window_start_ns;
	u64 window_events;
	u64 deepest_generation;
	u64 exec_migrations;
	u64 root_exited;
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 65536);
	__uint(map_flags, COMPAT_BPF_F_NO_PREALLOC);
	__type(key, u32);
	__type(value, struct profiler_fork_pid_t);
} fork_pid_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__uint(max_entries, 1);
	__type(key, u32);
	__type(value, struct profiler_fork_stats_t);
} fork_stats SEC(".maps");

/* Time-bucketed counters avoid racy shared-window resets while remaining
 * compatible with the project's BPF v1 instruction target. */
struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__uint(max_entries, 4);
	__type(key, u64);
	__type(value, u64);
} fork_rate_map SEC(".maps");

static __always_inline struct profiler_fork_stats_t *profiler_fork_stats(void)
{
	u32 zero = 0;
	return bpf_map_lookup_elem(&fork_stats, &zero);
}

static __always_inline struct profiler_fork_pid_t *profiler_fork_lookup(u32 pid)
{
	return bpf_map_lookup_elem(&fork_pid_map, &pid);
}

static __always_inline bool profiler_pid_is_tracked(u32 tgid, u32 pid)
{
	struct profiler_fork_stats_t *stats;

	if (profiler_filter_pid == 0)
		return true;
	if (tgid == profiler_filter_pid || pid == profiler_filter_pid) {
		if (!profiler_follow_forks)
			return true;
		stats = profiler_fork_stats();
		return !stats || stats->root_exited == 0;
	}
	if (!profiler_follow_forks)
		return false;
	if (profiler_fork_lookup(tgid))
		return true;
	if (pid != tgid && profiler_fork_lookup(pid))
		return true;
	return false;
}

/* A fixed one-second time bucket is verifier-friendly on the oldest supported
 * kernels. Atomic increments make all CPUs share one conservative allowance:
 * a racing reader can reject early, but cannot admit more than the limit. */
static __always_inline bool profiler_fork_rate_allowed(
	struct profiler_fork_stats_t *stats, u64 now)
{
	u64 bucket;
	u64 zero = 0;
	u64 *count_ptr;
	u64 count;
	u64 allowance;

	if (profiler_fork_rate == 0)
		return true;

	bucket = now / 1000000000ULL;
	bpf_map_update_elem(&fork_rate_map, &bucket, &zero, COMPAT_BPF_NOEXIST);
	count_ptr = bpf_map_lookup_elem(&fork_rate_map, &bucket);
	if (!count_ptr) {
		__sync_fetch_and_add(&stats->rejected_rate, 1);
		return false;
	}
	__sync_fetch_and_add(count_ptr, 1);
	count = *count_ptr;
	stats->window_start_ns = bucket * 1000000000ULL;
	stats->window_events = count;
	allowance = (u64)profiler_fork_rate + (u64)profiler_fork_burst;
	if (count > allowance) {
		__sync_fetch_and_add(&stats->rejected_rate, 1);
		return false;
	}
	return true;
}

SEC("raw_tracepoint/sched_process_fork")
int profiler_fork(struct bpf_raw_tracepoint_args *ctx)
{
	struct task_struct *parent_task;
	struct task_struct *child_task;
	struct profiler_fork_pid_t child = {};
	struct profiler_fork_pid_t *parent;
	struct profiler_fork_stats_t *stats;
	u32 parent_pid;
	u32 parent_tgid;
	u32 child_pid;
	u32 child_tgid;
	u64 now;

	if (!profiler_follow_forks || profiler_filter_pid == 0)
		return 0;

	parent_task = (struct task_struct *)ctx->args[0];
	child_task = (struct task_struct *)ctx->args[1];
	parent_pid = BPF_CORE_READ(parent_task, pid);
	parent_tgid = BPF_CORE_READ(parent_task, tgid);
	child_pid = BPF_CORE_READ(child_task, pid);
	child_tgid = BPF_CORE_READ(child_task, tgid);
	stats = profiler_fork_stats();
	if (!stats)
		return 0;
	if (parent_pid == profiler_filter_pid || parent_tgid == profiler_filter_pid) {
		if (stats->root_exited != 0)
			return 0;
		child.root_pid = profiler_filter_pid;
	} else {
		parent = profiler_fork_lookup(parent_pid);
		if (!parent && parent_tgid != parent_pid)
			parent = profiler_fork_lookup(parent_tgid);
		if (!parent)
			return 0;
		child.root_pid = parent->root_pid;
		child.parent_pid = parent->parent_pid;
		child.generation = parent->generation;
	}

	if (child_pid != child_tgid) {
		child.flags = PROFILER_FORK_F_THREAD;
	} else {
		child.parent_pid = parent_tgid;
		child.generation++;
	}

	if (profiler_fork_lookup(child_pid)) {
		__sync_fetch_and_add(&stats->duplicate, 1);
		return 0;
	}

	now = bpf_ktime_get_ns();
	if (!profiler_fork_rate_allowed(stats, now))
		return 0;
	/* Reserve before insertion. Concurrent CPUs may briefly push active over
	 * the limit, but any observer above the limit rolls itself back and cannot
	 * create a map entry. No returned atomic value is required. */
	__sync_fetch_and_add(&stats->active, 1);
	if (stats->active > profiler_fork_max_pids) {
		__sync_fetch_and_add(&stats->active, (u64)-1);
		__sync_fetch_and_add(&stats->rejected_limit, 1);
		return 0;
	}

	child.born_ns = now;
	if (bpf_map_update_elem(&fork_pid_map, &child_pid, &child,
				COMPAT_BPF_NOEXIST) != 0) {
		__sync_fetch_and_add(&stats->active, (u64)-1);
		__sync_fetch_and_add(&stats->update_failures, 1);
		return 0;
	}

	__sync_fetch_and_add(&stats->accepted, 1);
	if (child.generation > stats->deepest_generation)
		stats->deepest_generation = child.generation;
	return 0;
}

SEC("tracepoint/sched/sched_process_exec")
int profiler_exec(struct trace_event_raw_sched_process_exec *ctx)
{
	struct profiler_fork_pid_t migrated = {};
	struct profiler_fork_pid_t *old_entry;
	struct profiler_fork_stats_t *stats;
	u32 pid;
	u32 old_pid;
	bool target_exists;

	if (!profiler_follow_forks)
		return 0;

	pid = ctx->pid;
	old_pid = ctx->old_pid;
	if (pid == old_pid)
		return 0;

	old_entry = profiler_fork_lookup(old_pid);
	if (!old_entry)
		return 0;
	__builtin_memcpy(&migrated, old_entry, sizeof(migrated));
	stats = profiler_fork_stats();

	/* When a root worker execs, it assumes the implicit root key. Only its
	 * explicit worker entry needs to be removed. */
	if (pid == profiler_filter_pid) {
		if (bpf_map_delete_elem(&fork_pid_map, &old_pid) == 0 && stats) {
			__sync_fetch_and_add(&stats->active, (u64)-1);
			__sync_fetch_and_add(&stats->exec_migrations, 1);
		}
		return 0;
	}

	target_exists = profiler_fork_lookup(pid) != NULL;
	if (bpf_map_delete_elem(&fork_pid_map, &old_pid) != 0)
		return 0;

	if (!target_exists) {
		migrated.flags &= ~PROFILER_FORK_F_THREAD;
		/* Delete before insert so migration still works at map capacity. Restore
		 * the old record if the new key cannot be materialized. */
		if (bpf_map_update_elem(&fork_pid_map, &pid, &migrated,
					COMPAT_BPF_NOEXIST) != 0) {
			bpf_map_update_elem(&fork_pid_map, &old_pid, &migrated,
					    COMPAT_BPF_NOEXIST);
			if (stats)
				__sync_fetch_and_add(&stats->update_failures, 1);
			return 0;
		}
	}

	if (stats) {
		if (target_exists)
			__sync_fetch_and_add(&stats->active, (u64)-1);
		__sync_fetch_and_add(&stats->exec_migrations, 1);
	}
	return 0;
}

SEC("raw_tracepoint/sched_process_exit")
int profiler_exit(struct bpf_raw_tracepoint_args *ctx)
{
	struct task_struct *task;
	struct profiler_fork_stats_t *stats;
	u64 pid_tgid;
	u32 pid;
	u32 tgid;
	int live;

	if (!profiler_follow_forks)
		return 0;
	pid_tgid = bpf_get_current_pid_tgid();
	pid = (u32)pid_tgid;
	tgid = pid_tgid >> 32;
	stats = profiler_fork_stats();
	if (tgid == profiler_filter_pid) {
		/* A process leader may exit while sibling threads still run. signal->live
		 * reaches zero only when the whole thread group has gone, at which point
		 * the numeric root PID can eventually be reused by an unrelated task. */
		task = (struct task_struct *)ctx->args[0];
		live = BPF_CORE_READ(task, signal, live.counter);
		if (stats && live == 0)
			stats->root_exited = 1;
		if (pid == profiler_filter_pid)
			return 0;
	}
	if (!profiler_fork_lookup(pid))
		return 0;

	if (bpf_map_delete_elem(&fork_pid_map, &pid) == 0) {
		if (stats) {
			__sync_fetch_and_add(&stats->active, (u64)-1);
			__sync_fetch_and_add(&stats->exited, 1);
		}
	}
	return 0;
}

/*
 * profiler_should_trace - check if current process should be traced.
 * Returns true if should trace, false otherwise.
 */
static __always_inline bool profiler_should_trace(u64 pid_tgid)
{
	u32 tgid = pid_tgid >> 32;
	u32 pid = pid_tgid & 0xffffffffUL;

	if (profiler_filter_css != 0) {
		u64 css = current_task_memory_css_addr();
		if (css != profiler_filter_css)
			return false;
	}

	if (profiler_filter_pid != 0) {
		if (profiler_follow_forks) {
			if (!profiler_pid_is_tracked(tgid, pid))
				return false;
		} else if (profiler_filter_threads) {
			if (pid != profiler_filter_pid)
				return false;
		} else {
			if (tgid != profiler_filter_pid)
				return false;
		}
	}

	return true;
}

/*
 * profiler_should_trace_cpu - CPU profiler specific trace check.
 * Also checks idle class and cpu cgroup subsystem.
 */
static __always_inline bool profiler_should_trace_cpu(u64 pid_tgid, u64 cpu_css, u64 sched_class)
{
	u32 tgid = pid_tgid >> 32;
	u32 pid = pid_tgid & 0xffffffffUL;

	if (profiler_filter_css != 0 && profiler_filter_css != cpu_css)
		return false;

	if (profiler_filter_pid != 0 && !profiler_pid_is_tracked(tgid, pid))
		return false;

	if (profiler_idle_class_addr != 0 && sched_class == profiler_idle_class_addr)
		return false;

	if (tgid == 0 && pid == 0)
		return false;

	return true;
}

/*
 * profiler_should_sample - probabilistic sampling.
 * Returns true if should sample, false if skip.
 */
static __always_inline bool profiler_should_sample(void)
{
	if (profiler_sampling_prob >= 100)
		return true;
	return (bpf_get_prandom_u32() % 100) < profiler_sampling_prob;
}

/*
 * profiler_fill_event_base - fill base event information.
 * Returns 0 on success, -1 if both stacks failed.
 */
static __always_inline int profiler_fill_event_base(
	struct profiler_event_base_t *event,
	u64 pid_tgid,
	void *ctx,
	void *stack_map)
{
	event->pid_tgid = pid_tgid;
	bpf_get_current_comm(&event->comm, sizeof(event->comm));

	event->userstack = bpf_get_stackid(ctx, stack_map, USER_STACKID_FLAGS);
	event->kernstack = bpf_get_stackid(ctx, stack_map, KERN_STACKID_FLAGS);

	if (event->userstack < 0 && event->kernstack < 0)
		return -1;

	return 0;
}

static __always_inline void profiler_copy_event_base(
	struct profiler_event_base_t *dst,
	const struct profiler_event_base_t *src)
{
	dst->pid_tgid = src->pid_tgid;
	dst->kernstack = src->kernstack;
	dst->userstack = src->userstack;
	__builtin_memcpy(&dst->comm, &src->comm, sizeof(dst->comm));
}

static __always_inline struct profiler_event_base_t *profiler_prepare_event_base(
	void *event_map,
	u64 pid_tgid,
	void *ctx,
	void *stack_map)
{
	u32 idx = 0;
	struct profiler_event_base_t *event = bpf_map_lookup_elem(event_map, &idx);
	if (!event)
		return NULL;

	__builtin_memset(event, 0, sizeof(*event));

	if (profiler_fill_event_base(event, pid_tgid, ctx, stack_map) < 0)
		return NULL;

	return event;
}

static __always_inline void profiler_emit_event(
	void *ctx,
	void *output,
	u64 *sample_count_ptr,
	void *event,
	u64 event_size)
{
	/*
	 * Global ARRAY + atomic add is intentional; do NOT switch to PERCPU.
	 * See comment in original code for details.
	 */
	__sync_fetch_and_add(sample_count_ptr, 1);
	bpf_perf_event_output(ctx, output, COMPAT_BPF_F_CURRENT_CPU,
	                      event, event_size);
}

/*
 * DEFINE_PROFILER_MAPS - define all standard profiler maps.
 * Usage: DEFINE_PROFILER_MAPS(struct profiler_event_base_t);
 */
#define DEFINE_PROFILER_MAPS(event_type) \
struct { \
	__uint(type, BPF_MAP_TYPE_ARRAY); \
	__type(key, u32); \
	__type(value, u64); \
	__uint(max_entries, PROFILER_STATE_CNT); \
} profiler_state_map SEC(".maps"); \
\
struct { \
	__uint(type, BPF_MAP_TYPE_STACK_TRACE); \
	__uint(key_size, sizeof(u32)); \
	__uint(value_size, PERF_MAX_STACK_DEPTH * sizeof(u64)); \
	__uint(max_entries, STACK_MAP_ENTRIES); \
} stack_map_a SEC(".maps"); \
\
struct { \
	__uint(type, BPF_MAP_TYPE_STACK_TRACE); \
	__uint(key_size, sizeof(u32)); \
	__uint(value_size, PERF_MAX_STACK_DEPTH * sizeof(u64)); \
	__uint(max_entries, STACK_MAP_ENTRIES); \
} stack_map_b SEC(".maps"); \
\
struct { \
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY); \
	__uint(key_size, sizeof(int)); \
	__uint(value_size, sizeof(u32)); \
} profiler_output_a SEC(".maps"); \
\
struct { \
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY); \
	__uint(key_size, sizeof(int)); \
	__uint(value_size, sizeof(u32)); \
} profiler_output_b SEC(".maps"); \
\
struct { \
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY); \
	__uint(key_size, sizeof(u32)); \
	__uint(value_size, sizeof(event_type)); \
	__uint(max_entries, 1); \
} event_buf SEC(".maps")

/*
 * DEFINE_PROFILER_PAGE_TRACKING_MAP - define page tracking map.
 * Used by physical_alloc profiler.
 */
#define DEFINE_PROFILER_PAGE_TRACKING_MAP() \
struct { \
	__uint(type, BPF_MAP_TYPE_HASH); \
	__uint(max_entries, 1 << 20); \
	__type(key, u64); \
	__type(value, struct profiler_event_base_t); \
} page_to_stackid SEC(".maps")

/*
 * SELECT_PROFILER_AB - select A/B buffer based on transfer count parity.
 */
#define SELECT_PROFILER_AB() \
do { \
	if (((*(transfer_count_ptr)) & 0x1ULL) == 0) { \
		select_profiler_sample_count_ptr = sample_count_ptrs[0]; \
		select_profiler_stack_map = (void *)&stack_map_a; \
		select_profiler_output = (void *)&profiler_output_a; \
	} else { \
		select_profiler_sample_count_ptr = sample_count_ptrs[1]; \
		select_profiler_stack_map = (void *)&stack_map_b; \
		select_profiler_output = (void *)&profiler_output_b; \
	} \
} while(0)

/*
 * profiler_init_state - initialize profiler state pointers.
 */
static __always_inline bool profiler_init_state(void *state_map, u64 **transfer_ptr, u64 **sample_ptrs)
{
	u32 idx = PROFILER_STATE_TRANSFER_CNT_IDX;

	*transfer_ptr = bpf_map_lookup_elem(state_map, &idx);
	idx = PROFILER_STATE_SAMPLE_CNT_A_IDX;
	sample_ptrs[0] = bpf_map_lookup_elem(state_map, &idx);
	idx = PROFILER_STATE_SAMPLE_CNT_B_IDX;
	sample_ptrs[1] = bpf_map_lookup_elem(state_map, &idx);

	return *transfer_ptr && sample_ptrs[0] && sample_ptrs[1];
}

#endif /* __BPF_PROFILER_H__ */

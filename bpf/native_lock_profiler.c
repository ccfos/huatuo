#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_profiler.h"

char __license[] SEC("license") = "Dual MIT/GPL";

enum profiler_lock_type {
	PROFILER_LOCK_MUTEX = 1,
	PROFILER_LOCK_SPINLOCK = 2,
	PROFILER_LOCK_RWLOCK = 3,
};

#define PROFILER_LOCK_TYPE_BIT(type) (1U << ((type) - 1))

/* Flags exported by lock:contention_begin since Linux 5.19. */
#define LCB_F_SPIN (1U << 0)
#define LCB_F_READ (1U << 1)
#define LCB_F_WRITE (1U << 2)
#define LCB_F_MUTEX (1U << 5)

static volatile const u64 profiler_lock_min_wait_ns = 1000;
static volatile const u32 profiler_lock_type_mask =
	PROFILER_LOCK_TYPE_BIT(PROFILER_LOCK_MUTEX) |
	PROFILER_LOCK_TYPE_BIT(PROFILER_LOCK_SPINLOCK) |
	PROFILER_LOCK_TYPE_BIT(PROFILER_LOCK_RWLOCK);

struct lock_start_t {
	u64 started_ns;
	u64 lock;
	u8 lock_type;
	u8 pad[7];
};

/* Slow-path kretprobes do not carry the lock address, so key them by type. */
struct lock_start_key_t {
	u64 pid_tgid;
	u8 lock_type;
	u8 pad[7];
};

/* contention_end does carry the address, but not the begin event's flags. */
struct contention_start_key_t {
	u64 pid_tgid;
	u64 lock;
};

struct lock_stat_key_t {
	u64 pid_tgid;
	char comm[COMPAT_TASK_COMM_LEN];
	u64 lock;
	int kernstack;
	int userstack;
	u8 lock_type;
	u8 pad[7];
};

struct lock_stat_t {
	u64 wait_ns;
	u64 contended;
};

_Static_assert(sizeof(struct lock_stat_key_t) == 48,
	       "lock stat key ABI changed");
_Static_assert(sizeof(struct lock_stat_t) == 16,
	       "lock stat value ABI changed");

struct {
	/* LRU bounds stale entries if a kretprobe instance is missed. */
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__uint(max_entries, 65536);
	__type(key, struct lock_start_key_t);
	__type(value, struct lock_start_t);
} lock_starts SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__uint(max_entries, 65536);
	__type(key, struct contention_start_key_t);
	__type(value, struct lock_start_t);
} contention_starts SEC(".maps");

/*
 * Aggregate in BPF instead of sending one perf event per contention.  The A/B
 * maps follow the stack-map parity, so userspace can flip once, then drain a
 * stable stats map without stopping collection.
 */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 32768);
	__type(key, struct lock_stat_key_t);
	__type(value, struct lock_stat_t);
} lock_stats_a SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 32768);
	__type(key, struct lock_stat_key_t);
	__type(value, struct lock_stat_t);
} lock_stats_b SEC(".maps");

/* Reuse the profiler state and dual stack maps; perf output maps stay idle. */
DEFINE_PROFILER_MAPS(struct profiler_event_base_t);

static __always_inline bool lock_type_enabled(u8 lock_type)
{
	return profiler_lock_type_mask & PROFILER_LOCK_TYPE_BIT(lock_type);
}

static __always_inline bool lock_matches_target(u64 pid_tgid)
{
	/* Lock profiling resolves the CPU CSS in userspace. */
	if (profiler_filter_css != 0 &&
	    profiler_filter_css != current_task_cpu_css_addr())
		return false;

	return profiler_matches_dimensions(pid_tgid);
}

static __always_inline u8 contention_lock_type(u32 flags)
{
	if (flags & LCB_F_MUTEX)
		return PROFILER_LOCK_MUTEX;
	if ((flags & LCB_F_SPIN) && (flags & (LCB_F_READ | LCB_F_WRITE)))
		return PROFILER_LOCK_RWLOCK;
	if (flags & LCB_F_SPIN)
		return PROFILER_LOCK_SPINLOCK;
	return 0;
}

static __always_inline int aggregate_lock_wait(void *ctx,
					       const struct lock_start_t *start)
{
	u64 wait_ns = bpf_ktime_get_ns() - start->started_ns;
	if (wait_ns < profiler_lock_min_wait_ns)
		return 0;

	u32 state_idx = PROFILER_STATE_TRANSFER_CNT_IDX;
	u64 *transfer_count_ptr =
		bpf_map_lookup_elem(&profiler_state_map, &state_idx);
	if (!transfer_count_ptr)
		return 0;

	void *stack_map;
	void *stats_map;
	if ((*transfer_count_ptr & 1ULL) == 0) {
		stack_map = (void *)&stack_map_a;
		stats_map = (void *)&lock_stats_a;
	} else {
		stack_map = (void *)&stack_map_b;
		stats_map = (void *)&lock_stats_b;
	}

	struct lock_stat_key_t key = {
		.pid_tgid = bpf_get_current_pid_tgid(),
		.lock = start->lock,
		.lock_type = start->lock_type,
		.kernstack = -1,
		.userstack = -1,
	};
	bpf_get_current_comm(&key.comm, sizeof(key.comm));
	key.userstack = bpf_get_stackid(ctx, stack_map, USER_STACKID_FLAGS);
	key.kernstack = bpf_get_stackid(ctx, stack_map, KERN_STACKID_FLAGS);
	if (key.userstack < 0 && key.kernstack < 0)
		return 0;

	struct lock_stat_t zero = {};
	bpf_map_update_elem(stats_map, &key, &zero, COMPAT_BPF_NOEXIST);
	struct lock_stat_t *stat = bpf_map_lookup_elem(stats_map, &key);
	if (!stat)
		return 0;

	__sync_fetch_and_add(&stat->wait_ns, wait_ns);
	__sync_fetch_and_add(&stat->contended, 1);
	return 0;
}

static __always_inline int trace_lock_enter(struct pt_regs *ctx, u8 lock_type)
{
	u64 pid_tgid = bpf_get_current_pid_tgid();
	if (!lock_type_enabled(lock_type) || !lock_matches_target(pid_tgid))
		return 0;

	struct lock_start_key_t key = {
		.pid_tgid = pid_tgid,
		.lock_type = lock_type,
	};
	struct lock_start_t start = {
		.started_ns = bpf_ktime_get_ns(),
		.lock = PT_REGS_PARM1(ctx),
		.lock_type = lock_type,
	};
	bpf_map_update_elem(&lock_starts, &key, &start, COMPAT_BPF_ANY);
	return 0;
}

static __always_inline int trace_lock_exit(struct pt_regs *ctx, u8 lock_type,
					    bool check_return)
{
	u64 pid_tgid = bpf_get_current_pid_tgid();
	struct lock_start_key_t key = {
		.pid_tgid = pid_tgid,
		.lock_type = lock_type,
	};
	struct lock_start_t *found = bpf_map_lookup_elem(&lock_starts, &key);
	if (!found)
		return 0;

	struct lock_start_t start = *found;
	bpf_map_delete_elem(&lock_starts, &key);
	if (check_return && PT_REGS_RC(ctx) != 0)
		return 0;

	return aggregate_lock_wait(ctx, &start);
}

SEC("kprobe/huatuo_mutex_lock_slowpath")
int trace_mutex_lock(struct pt_regs *ctx)
{
	return trace_lock_enter(ctx, PROFILER_LOCK_MUTEX);
}

SEC("kretprobe/huatuo_mutex_lock_slowpath")
int trace_mutex_lock_return(struct pt_regs *ctx)
{
	return trace_lock_exit(ctx, PROFILER_LOCK_MUTEX, false);
}

SEC("kretprobe/huatuo_mutex_lock_interruptible_slowpath")
int trace_mutex_lock_interruptible_return(struct pt_regs *ctx)
{
	return trace_lock_exit(ctx, PROFILER_LOCK_MUTEX, true);
}

SEC("kprobe/huatuo_spin_lock_slowpath")
int trace_spin_lock(struct pt_regs *ctx)
{
	return trace_lock_enter(ctx, PROFILER_LOCK_SPINLOCK);
}

SEC("kretprobe/huatuo_spin_lock_slowpath")
int trace_spin_lock_return(struct pt_regs *ctx)
{
	return trace_lock_exit(ctx, PROFILER_LOCK_SPINLOCK, false);
}

SEC("kprobe/huatuo_rw_lock_slowpath")
int trace_rw_lock(struct pt_regs *ctx)
{
	return trace_lock_enter(ctx, PROFILER_LOCK_RWLOCK);
}

SEC("kretprobe/huatuo_rw_lock_slowpath")
int trace_rw_lock_return(struct pt_regs *ctx)
{
	return trace_lock_exit(ctx, PROFILER_LOCK_RWLOCK, false);
}

/* Stable tracepoint payload layout from include/trace/events/lock.h. */
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

SEC("tracepoint/lock/contention_begin")
int trace_lock_contention_begin(struct lock_contention_begin_ctx *ctx)
{
	u8 lock_type = contention_lock_type(ctx->flags);
	u64 pid_tgid = bpf_get_current_pid_tgid();
	if (!lock_type || !lock_type_enabled(lock_type) ||
	    !lock_matches_target(pid_tgid))
		return 0;

	struct contention_start_key_t key = {
		.pid_tgid = pid_tgid,
		.lock = ctx->lock_addr,
	};
	struct lock_start_t start = {
		.started_ns = bpf_ktime_get_ns(),
		.lock = ctx->lock_addr,
		.lock_type = lock_type,
	};
	bpf_map_update_elem(&contention_starts, &key, &start, COMPAT_BPF_ANY);
	return 0;
}

SEC("tracepoint/lock/contention_end")
int trace_lock_contention_end(struct lock_contention_end_ctx *ctx)
{
	u64 pid_tgid = bpf_get_current_pid_tgid();
	struct contention_start_key_t key = {
		.pid_tgid = pid_tgid,
		.lock = ctx->lock_addr,
	};
	struct lock_start_t *found =
		bpf_map_lookup_elem(&contention_starts, &key);
	if (!found)
		return 0;

	struct lock_start_t start = *found;
	bpf_map_delete_elem(&contention_starts, &key);
	if (ctx->ret != 0)
		return 0;

	return aggregate_lock_wait(ctx, &start);
}

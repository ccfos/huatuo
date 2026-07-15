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

static volatile const u64 profiler_lock_min_wait_ns = 1000;

struct lock_start_t {
	u64 started_ns;
	u64 lock;
	u8 lock_type;
	u8 pad[7];
};

/*
 * A task can enter one profiled lock implementation while acquiring another
 * lock type (mutex slow paths routinely take spinlocks).  Keying only by
 * pid_tgid makes the inner acquisition overwrite the outer one.  Keep one
 * independent slot per task and lock type instead.
 */
struct lock_start_key_t {
	u64 pid_tgid;
	u8 lock_type;
	u8 pad[7];
};

struct lock_event_t {
	struct profiler_event_base_t base;
	u64 lock;
	u64 wait_ns;
	u32 contended;
	u8 lock_type;
	u8 pad[3];
};

_Static_assert(sizeof(struct profiler_event_base_t) == 40,
	       "profiler event base ABI changed");
_Static_assert(sizeof(struct lock_event_t) == 64,
	       "lock event ABI changed");

struct {
	/* LRU bounds stale entries if the kernel misses a kretprobe instance under
	 * extreme concurrency. A later entry for the same task/type overwrites its
	 * stale start immediately. */
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__uint(max_entries, 65536);
	__type(key, struct lock_start_key_t);
	__type(value, struct lock_start_t);
} lock_starts SEC(".maps");

DEFINE_PROFILER_MAPS(struct lock_event_t);

static __always_inline int trace_lock_enter(struct pt_regs *ctx, u8 lock_type)
{
	u64 pid_tgid = bpf_get_current_pid_tgid();
	/* Lock profiling resolves a CPU CSS in userspace, so it must compare the
	 * same subsystem here.  profiler_should_trace() intentionally uses the
	 * memory CSS for the native memory profilers. */
	if (profiler_filter_css != 0 &&
	    profiler_filter_css != current_task_cpu_css_addr())
		return 0;
	if (!profiler_matches_dimensions(pid_tgid))
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

static __always_inline int trace_lock_exit(struct pt_regs *ctx, u8 expected_type)
{
	u64 pid_tgid = bpf_get_current_pid_tgid();
	struct lock_start_key_t key = {
		.pid_tgid = pid_tgid,
		.lock_type = expected_type,
	};
	struct lock_start_t *start = bpf_map_lookup_elem(&lock_starts, &key);
	if (!start || start->lock_type != expected_type)
		return 0;

	u64 lock = start->lock;
	u64 wait_ns = bpf_ktime_get_ns() - start->started_ns;
	bpf_map_delete_elem(&lock_starts, &key);
	if (wait_ns < profiler_lock_min_wait_ns)
		return 0;

	u64 *transfer_count_ptr;
	u64 *sample_count_ptrs[2];
	void *select_profiler_stack_map;
	void *select_profiler_output;
	u64 *select_profiler_sample_count_ptr;
	if (!profiler_init_state(&profiler_state_map, &transfer_count_ptr, sample_count_ptrs))
		return 0;

	SELECT_PROFILER_AB();

	u32 idx = 0;
	struct lock_event_t *event = bpf_map_lookup_elem(&event_buf, &idx);
	if (!event)
		return 0;
	__builtin_memset(event, 0, sizeof(*event));
	if (profiler_fill_event_base(&event->base, pid_tgid, ctx, select_profiler_stack_map) < 0)
		return 0;

	event->base.value = wait_ns;
	event->lock = lock;
	event->wait_ns = wait_ns;
	event->contended = 1;
	event->lock_type = expected_type;
	profiler_emit_event(ctx, select_profiler_output,
	                    select_profiler_sample_count_ptr, event, sizeof(*event));
	return 0;
}

SEC("kprobe/huatuo_mutex_lock")
int trace_mutex_lock(struct pt_regs *ctx)
{
	return trace_lock_enter(ctx, PROFILER_LOCK_MUTEX);
}

SEC("kretprobe/huatuo_mutex_lock")
int trace_mutex_lock_return(struct pt_regs *ctx)
{
	return trace_lock_exit(ctx, PROFILER_LOCK_MUTEX);
}

SEC("kprobe/huatuo_spin_lock")
int trace_spin_lock(struct pt_regs *ctx)
{
	return trace_lock_enter(ctx, PROFILER_LOCK_SPINLOCK);
}

SEC("kretprobe/huatuo_spin_lock")
int trace_spin_lock_return(struct pt_regs *ctx)
{
	return trace_lock_exit(ctx, PROFILER_LOCK_SPINLOCK);
}

SEC("kprobe/huatuo_rw_lock")
int trace_rw_lock(struct pt_regs *ctx)
{
	return trace_lock_enter(ctx, PROFILER_LOCK_RWLOCK);
}

SEC("kretprobe/huatuo_rw_lock")
int trace_rw_lock_return(struct pt_regs *ctx)
{
	return trace_lock_exit(ctx, PROFILER_LOCK_RWLOCK);
}

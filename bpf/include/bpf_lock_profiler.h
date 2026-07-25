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
	u8 access;
	u8 pad[7];
};

struct lock_contention_event_t {
	struct profiler_event_base_t base;
	u64 lock;
	u8 access;
	u8 pad[7];
};

#ifdef HUATUO_LOCK_PROFILER_SPIN
struct spin_wait_start_t {
	struct lock_contention_event_t event;
	u64 started_ns;
	u64 transfer_count;
	u8 active;
};

/*
 * A spinning task cannot migrate. A per-CPU slot avoids a global hash update
 * on the spinlock hot path and bounds begin/end correlation to one record.
 */
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(max_entries, 1);
	__type(key, u32);
	__type(value, struct spin_wait_start_t);
} spin_wait_starts SEC(".maps");
#else
/*
 * A blocked task can migrate. A task-keyed LRU map keeps enter/exit
 * correlation valid across migration and bounds stale kretprobe entries.
 */
struct {
	__uint(type, BPF_MAP_TYPE_LRU_HASH);
	__uint(max_entries, 65536);
	__type(key, u64);
	__type(value, struct lock_wait_start_t);
} lock_wait_starts SEC(".maps");
#endif

DEFINE_PROFILER_MAPS(struct lock_contention_event_t);

#ifndef HUATUO_LOCK_PROFILER_SPIN
static __always_inline int lock_wait_begin(u64 pid_tgid, u64 lock, u8 access)
{
	u64 cpu_css = 0;
	if (profiler_filter_css != 0)
		cpu_css = current_task_cpu_css_addr();
	if (!profiler_should_trace(pid_tgid, cpu_css))
		return 0;

	struct lock_wait_start_t start = {
		.started_ns = bpf_ktime_get_ns(),
		.lock = lock,
		.access = access,
	};
	bpf_map_update_elem(&lock_wait_starts, &pid_tgid, &start,
			    COMPAT_BPF_ANY);
	return 0;
}

static __always_inline int lock_wait_end(void *ctx, u64 pid_tgid,
					 u64 lock, bool success)
{
	struct lock_wait_start_t *found =
		bpf_map_lookup_elem(&lock_wait_starts, &pid_tgid);
	if (!found)
		return 0;
	/*
	 * contention_end has no type flags. Ignore nested contention from a
	 * different lock instead of consuming the outer start record.
	 */
	if (lock != 0 && found->lock != lock)
		return 0;

	struct lock_wait_start_t start = *found;
	bpf_map_delete_elem(&lock_wait_starts, &pid_tgid);
	if (!success)
		return 0;

	u64 wait_ns = bpf_ktime_get_ns() - start.started_ns;
	if (wait_ns < lock_wait_threshold_ns)
		return 0;

	u64 *transfer_count_ptr;
	u64 *sample_count_ptrs[2];
	void *select_profiler_stack_map;
	void *select_profiler_output;
	u64 *select_profiler_sample_count_ptr;

	if (!profiler_init_state(&profiler_state_map, &transfer_count_ptr,
				 sample_count_ptrs))
		return 0;

	SELECT_PROFILER_AB();

	u32 idx = 0;
	struct lock_contention_event_t *event =
		bpf_map_lookup_elem(&event_buf, &idx);
	if (!event)
		return 0;

	__builtin_memset(event, 0, sizeof(*event));
	event->base.value = wait_ns;
	event->lock = start.lock;
	event->access = start.access;
	if (profiler_fill_event_base(&event->base, pid_tgid, ctx,
				     select_profiler_stack_map) < 0)
		return 0;

	profiler_emit_event(ctx, select_profiler_output,
			    select_profiler_sample_count_ptr, event,
			    sizeof(*event));
	return 0;
}
#else
static __always_inline int spin_wait_begin(void *ctx, u64 lock)
{
	u64 pid_tgid = bpf_get_current_pid_tgid();
	u64 cpu_css = 0;
	if (profiler_filter_css != 0)
		cpu_css = current_task_cpu_css_addr();
	if (!profiler_should_trace(pid_tgid, cpu_css))
		return 0;

	u64 *transfer_count_ptr;
	u64 *sample_count_ptrs[2];
	void *select_profiler_stack_map;

	if (!profiler_init_state(&profiler_state_map, &transfer_count_ptr,
				 sample_count_ptrs))
		return 0;

	u64 transfer_count = *transfer_count_ptr;
	if ((transfer_count & 1ULL) == 0)
		select_profiler_stack_map = (void *)&stack_map_a;
	else
		select_profiler_stack_map = (void *)&stack_map_b;

	u32 idx = 0;
	struct spin_wait_start_t *start =
		bpf_map_lookup_elem(&spin_wait_starts, &idx);
	if (!start)
		return 0;

	__builtin_memset(start, 0, sizeof(*start));
	start->event.lock = lock;
	start->started_ns = bpf_ktime_get_ns();
	if (profiler_fill_event_base(&start->event.base, pid_tgid, ctx,
				     select_profiler_stack_map) < 0)
		return 0;
	if (*transfer_count_ptr != transfer_count)
		return 0;

	start->transfer_count = transfer_count;
	start->active = 1;
	return 0;
}

static __always_inline int spin_wait_end(void *ctx, u64 lock, bool success)
{
	u32 idx = 0;
	struct spin_wait_start_t *start =
		bpf_map_lookup_elem(&spin_wait_starts, &idx);
	if (!start || !start->active || start->event.lock != lock)
		return 0;

	struct lock_contention_event_t event = start->event;
	u64 wait_ns = bpf_ktime_get_ns() - start->started_ns;
	u64 transfer_count = start->transfer_count;
	start->active = 0;
	if (!success || wait_ns < lock_wait_threshold_ns)
		return 0;

	u64 *transfer_count_ptr;
	u64 *sample_count_ptrs[2];
	if (!profiler_init_state(&profiler_state_map, &transfer_count_ptr,
				 sample_count_ptrs))
		return 0;

	/*
	 * A drain flipped the active stack/output pair during this wait. Drop
	 * this rare sample instead of publishing a stack ID in the wrong pair.
	 */
	if (*transfer_count_ptr != transfer_count)
		return 0;

	void *select_profiler_output;
	u64 *select_profiler_sample_count_ptr;
	if ((transfer_count & 1ULL) == 0) {
		select_profiler_output = (void *)&profiler_output_a;
		select_profiler_sample_count_ptr = sample_count_ptrs[0];
	} else {
		select_profiler_output = (void *)&profiler_output_b;
		select_profiler_sample_count_ptr = sample_count_ptrs[1];
	}

	event.base.value = wait_ns;
	profiler_emit_event(ctx, select_profiler_output,
			    select_profiler_sample_count_ptr, &event,
			    sizeof(event));
	return 0;
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

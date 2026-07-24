#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_dbg.h"
#include "bpf_map.h"
#include "bpf_profiler.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define TASK_RUNNING 0
#define OFFCPU_STATE_ENTRIES 32768
#define OFFCPU_EVENT_ABI_VERSION 1

enum offcpu_metric {
	OFFCPU_METRIC_TOTAL = 0,
	OFFCPU_METRIC_BLOCKED,
	OFFCPU_METRIC_RUNNABLE,
};

enum offcpu_event_kind {
	OFFCPU_EVENT_BLOCKED = 1,
	OFFCPU_EVENT_RUNQUEUE,
};

enum offcpu_event_flag {
	OFFCPU_FLAG_PREEMPTED = 1 << 0,
	OFFCPU_FLAG_YIELDED = 1 << 1,
	OFFCPU_FLAG_MISSED_WAKEUP = 1 << 2,
};

enum offcpu_phase {
	OFFCPU_PHASE_BLOCKED = 1,
	OFFCPU_PHASE_RUNNABLE,
};

enum offcpu_stat {
	OFFCPU_STAT_TRACKED = 0,
	OFFCPU_STAT_BLOCKED_EMITTED,
	OFFCPU_STAT_RUNQUEUE_EMITTED,
	OFFCPU_STAT_BELOW_THRESHOLD,
	OFFCPU_STAT_ABOVE_THRESHOLD,
	OFFCPU_STAT_STACK_ERROR,
	OFFCPU_STAT_STATE_ERROR,
	OFFCPU_STAT_OUTPUT_ERROR,
	OFFCPU_STAT_MISSED_WAKEUP,
	OFFCPU_STAT_EXIT_CLEANUP,
	OFFCPU_STAT_COUNT,
};

static volatile const __u32 profiler_offcpu_metric = OFFCPU_METRIC_TOTAL;
static volatile const __u64 profiler_offcpu_min_ns = 1000000;
static volatile const __u64 profiler_offcpu_max_ns = 0;

BPF_DBG_MAP(native_cpu_offcpu_dbg);

/* Fixed ABI v1: 40-byte base + 24-byte off-CPU metadata. */
struct offcpu_event_t {
	struct profiler_event_base_t base;
	__u64 start_ns;
	__u64 end_ns;
	__u32 cpu;
	__u16 abi_version;
	__u8 kind;
	__u8 flags;
};

struct offcpu_state_t {
	struct profiler_event_base_t base;
	__u64 phase_start_ns;
	__u8 phase;
	__u8 flags;
	__u8 pad0[6];
};

/*
 * A single stack map is intentional. An off-CPU interval can outlive any
 * userspace drain period; rotating A/B stack maps could resolve a delayed
 * stack ID against the wrong map.
 */
struct {
	__uint(type, BPF_MAP_TYPE_STACK_TRACE);
	__uint(key_size, sizeof(__u32));
	__uint(value_size, PERF_MAX_STACK_DEPTH * sizeof(__u64));
	__uint(max_entries, STACK_MAP_ENTRIES);
} offcpu_stack_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(__u32));
} offcpu_output SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__type(key, __u32);
	__type(value, struct offcpu_event_t);
	__uint(max_entries, 1);
} offcpu_event_buf SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, __u64);
	__type(value, struct offcpu_state_t);
	__uint(max_entries, OFFCPU_STATE_ENTRIES);
} offcpu_states SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__type(key, __u32);
	__type(value, __u64);
	__uint(max_entries, OFFCPU_STAT_COUNT);
} offcpu_stats SEC(".maps");

struct task_struct___5_14 {
	unsigned int __state;
} __attribute__((preserve_access_index));

static __always_inline long offcpu_task_state(struct task_struct *task)
{
	if (!task)
		return -1;

	if (bpf_core_field_exists(task->state))
		return BPF_CORE_READ(task, state);

	struct task_struct___5_14 *new_task = (void *)task;
	return (long)BPF_CORE_READ(new_task, __state);
}

static __always_inline void offcpu_count(__u32 index)
{
	__u64 *value = bpf_map_lookup_elem(&offcpu_stats, &index);
	if (value)
		(*value)++;
}

static __always_inline bool offcpu_metric_enabled(__u8 kind)
{
	if (profiler_offcpu_metric == OFFCPU_METRIC_TOTAL)
		return true;
	if (profiler_offcpu_metric == OFFCPU_METRIC_BLOCKED)
		return kind == OFFCPU_EVENT_BLOCKED;
	return kind == OFFCPU_EVENT_RUNQUEUE;
}

static __always_inline void offcpu_emit(
	void *ctx,
	const struct offcpu_state_t *state,
	__u64 end_ns,
	__u8 kind,
	__u8 extra_flags)
{
	if (!offcpu_metric_enabled(kind) || end_ns <= state->phase_start_ns)
		return;

	__u64 duration = end_ns - state->phase_start_ns;
	if (duration < profiler_offcpu_min_ns) {
		offcpu_count(OFFCPU_STAT_BELOW_THRESHOLD);
		return;
	}
	if (profiler_offcpu_max_ns != 0 && duration > profiler_offcpu_max_ns) {
		offcpu_count(OFFCPU_STAT_ABOVE_THRESHOLD);
		return;
	}

	__u32 zero = 0;
	struct offcpu_event_t *event = bpf_map_lookup_elem(&offcpu_event_buf, &zero);
	if (!event)
		return;

	__builtin_memset(event, 0, sizeof(*event));
	profiler_copy_event_base(&event->base, &state->base);
	event->base.value = (__s64)duration;
	event->start_ns = state->phase_start_ns;
	event->end_ns = end_ns;
	event->cpu = bpf_get_smp_processor_id();
	event->abi_version = OFFCPU_EVENT_ABI_VERSION;
	event->kind = kind;
	event->flags = state->flags | extra_flags;

	long err = bpf_perf_event_output(ctx, &offcpu_output,
					 COMPAT_BPF_F_CURRENT_CPU, event, sizeof(*event));
	if (err < 0) {
		offcpu_count(OFFCPU_STAT_OUTPUT_ERROR);
		return;
	}

	offcpu_count(kind == OFFCPU_EVENT_BLOCKED ?
		      OFFCPU_STAT_BLOCKED_EMITTED : OFFCPU_STAT_RUNQUEUE_EMITTED);
}

static __always_inline int offcpu_wakeup(
	struct bpf_raw_tracepoint_args *ctx,
	struct task_struct *task)
{
	__u64 key = (__u64)task;
	if (!key)
		return 0;

	struct offcpu_state_t *state = bpf_map_lookup_elem(&offcpu_states, &key);
	if (!state || state->phase != OFFCPU_PHASE_BLOCKED)
		return 0;

	__u64 now = bpf_ktime_get_ns();
	offcpu_emit(ctx, state, now, OFFCPU_EVENT_BLOCKED, 0);

	/* Preserve the captured stack and begin measuring scheduler delay. */
	state->phase = OFFCPU_PHASE_RUNNABLE;
	state->phase_start_ns = now;
	return 0;
}

SEC("raw_tracepoint/sched_wakeup")
int native_cpu_offcpu_wakeup(struct bpf_raw_tracepoint_args *ctx)
{
	return offcpu_wakeup(ctx, (void *)ctx->args[0]);
}

SEC("raw_tracepoint/sched_wakeup_new")
int native_cpu_offcpu_wakeup_new(struct bpf_raw_tracepoint_args *ctx)
{
	return offcpu_wakeup(ctx, (void *)ctx->args[0]);
}

static __always_inline void offcpu_record_switch_out(
	struct bpf_raw_tracepoint_args *ctx,
	struct task_struct *prev,
	__u64 now)
{
	__u64 pid_tgid = bpf_get_current_pid_tgid();
	__u32 pid = (__u32)pid_tgid;
	__u32 tgid = pid_tgid >> 32;
	__u64 key = (__u64)prev;

	if (!key || (pid == 0 && tgid == 0) ||
	    !profiler_should_trace(pid_tgid, current_task_cpu_css_addr()))
		return;

	struct offcpu_state_t state = {};
	if (profiler_fill_event_base(&state.base, pid_tgid, ctx,
				     &offcpu_stack_map) < 0) {
		offcpu_count(OFFCPU_STAT_STACK_ERROR);
		return;
	}

	bool preempted = (__u64)ctx->args[0] != 0;
	long task_state = offcpu_task_state(prev);
	state.phase_start_ns = now;
	if (preempted || task_state == TASK_RUNNING) {
		state.phase = OFFCPU_PHASE_RUNNABLE;
		state.flags = preempted ? OFFCPU_FLAG_PREEMPTED : OFFCPU_FLAG_YIELDED;
	} else {
		state.phase = OFFCPU_PHASE_BLOCKED;
	}

	if (bpf_map_update_elem(&offcpu_states, &key, &state, BPF_ANY) < 0) {
		offcpu_count(OFFCPU_STAT_STATE_ERROR);
		return;
	}
	offcpu_count(OFFCPU_STAT_TRACKED);
}

static __always_inline void offcpu_finish_switch_in(
	struct bpf_raw_tracepoint_args *ctx,
	struct task_struct *next,
	__u64 now)
{
	__u64 key = (__u64)next;
	if (!key || BPF_CORE_READ(next, pid) == 0)
		return;

	struct offcpu_state_t *state = bpf_map_lookup_elem(&offcpu_states, &key);
	if (!state)
		return;

	if (state->phase == OFFCPU_PHASE_RUNNABLE) {
		offcpu_emit(ctx, state, now, OFFCPU_EVENT_RUNQUEUE, 0);
	} else {
		/* Preserve real elapsed time but make the missing wakeup explicit. */
		offcpu_emit(ctx, state, now, OFFCPU_EVENT_BLOCKED,
			    OFFCPU_FLAG_MISSED_WAKEUP);
		offcpu_count(OFFCPU_STAT_MISSED_WAKEUP);
	}
	bpf_map_delete_elem(&offcpu_states, &key);
}

SEC("raw_tracepoint/sched_switch")
int native_cpu_offcpu_switch(struct bpf_raw_tracepoint_args *ctx)
{
	struct task_struct *prev = (void *)ctx->args[1];
	struct task_struct *next = (void *)ctx->args[2];
	__u64 now = bpf_ktime_get_ns();

	/* Complete next before recording prev; sched_switch guarantees prev != next. */
	offcpu_finish_switch_in(ctx, next, now);
	offcpu_record_switch_out(ctx, prev, now);
	return 0;
}

static __always_inline int offcpu_cleanup_task(struct task_struct *task)
{
	__u64 key = (__u64)task;
	if (key && bpf_map_lookup_elem(&offcpu_states, &key)) {
		bpf_map_delete_elem(&offcpu_states, &key);
		offcpu_count(OFFCPU_STAT_EXIT_CLEANUP);
	}
	return 0;
}

SEC("raw_tracepoint/sched_process_exit")
int native_cpu_offcpu_exit(struct bpf_raw_tracepoint_args *ctx)
{
	return offcpu_cleanup_task((void *)ctx->args[0]);
}

SEC("raw_tracepoint/sched_process_free")
int native_cpu_offcpu_free(struct bpf_raw_tracepoint_args *ctx)
{
	return offcpu_cleanup_task((void *)ctx->args[0]);
}

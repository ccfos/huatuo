#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_dbg.h"
#include "bpf_map.h"
#include "bpf_profiler.h"
#include "bpf_sched.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define TASK_RUNNING 0

enum {
	OFFCPU_STATE_MAX_ENTRIES = 32768,
	OFFCPU_CPU_SET_WORD_BITS = 64,
	OFFCPU_CPU_SET_WORDS = 128,
};

enum offcpu_phase_filter {
	OFFCPU_PHASE_FILTER_ALL = 0,
	OFFCPU_PHASE_FILTER_BLOCKED,
	OFFCPU_PHASE_FILTER_RUNQUEUE,
};

static volatile const __u32 profiler_offcpu_phase = OFFCPU_PHASE_FILTER_ALL;
static volatile const __u64 profiler_offcpu_min_duration_ns = 1000000;
static volatile const __u32 profiler_offcpu_cpu_set_enabled = 0;
static volatile const __u32 profiler_offcpu_stats_enabled = 0;

BPF_DBG_MAP(native_cpu_dbg);

struct offcpu_state {
	struct profiler_event_base base;
	__u64 phase_start_ns;
	enum profiler_offcpu_event_kind kind;
	__u32 pad0;
};

/* A single stack map is intentional. An off-CPU interval can outlive any
 * userspace drain period; rotating A/B stack maps could resolve a delayed
 * stack ID against the wrong map.
 */
struct {
	__uint(type, BPF_MAP_TYPE_STACK_TRACE);
	__uint(key_size, sizeof(__u32));
	__uint(value_size, PERF_MAX_STACK_DEPTH * sizeof(__u64));
	__uint(max_entries, STACK_MAP_ENTRIES);
} stack_map_a SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(__u32));
} profiler_output_a SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__type(key, __u32);
	__type(value, __u64);
	__uint(max_entries, OFFCPU_CPU_SET_WORDS);
} offcpu_cpu_set SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__type(key, __u32);
	__type(value, struct profiler_offcpu_event);
	__uint(max_entries, 1);
} offcpu_event_buf SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, __u64);
	__type(value, struct offcpu_state);
	__uint(max_entries, OFFCPU_STATE_MAX_ENTRIES);
} offcpu_states SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__type(key, __u32);
	__type(value, __u64);
	__uint(max_entries, PROFILER_OFFCPU_STAT_MAX);
} offcpu_stats SEC(".maps");

static __always_inline void offcpu_stat_inc(enum profiler_offcpu_stat stat)
{
	__u64 *counter;

	if (!profiler_offcpu_stats_enabled)
		return;

	counter = bpf_map_lookup_elem(&offcpu_stats, &stat);
	if (counter)
		(*counter)++;
}

static __always_inline bool
offcpu_kind_enabled(enum profiler_offcpu_event_kind kind)
{
	if (profiler_offcpu_phase == OFFCPU_PHASE_FILTER_ALL)
		return true;
	if (profiler_offcpu_phase == OFFCPU_PHASE_FILTER_BLOCKED)
		return kind == PROFILER_OFFCPU_EVENT_BLOCKED;
	return kind != PROFILER_OFFCPU_EVENT_BLOCKED;
}

static __always_inline bool offcpu_cpu_selected(__u32 cpu)
{
	__u64 *mask;
	__u32 word;

	/* Keep the default path map-lookup free after constant rewriting. */
	if (!profiler_offcpu_cpu_set_enabled)
		return true;

	word = cpu / OFFCPU_CPU_SET_WORD_BITS;
	if (word >= OFFCPU_CPU_SET_WORDS)
		return false;

	mask = bpf_map_lookup_elem(&offcpu_cpu_set, &word);
	return mask && (*mask & (1ULL << (cpu % OFFCPU_CPU_SET_WORD_BITS)));
}

static __always_inline void
offcpu_emit_event(void *ctx, const struct offcpu_state *state, __u64 end_ns,
		  enum profiler_offcpu_event_kind kind)
{
	struct profiler_offcpu_event *event;
	__u64 duration;
	__u32 zero = 0;

	if (!offcpu_kind_enabled(kind) || end_ns <= state->phase_start_ns)
		return;

	duration = end_ns - state->phase_start_ns;
	if (duration < profiler_offcpu_min_duration_ns)
		return;

	event = bpf_map_lookup_elem(&offcpu_event_buf, &zero);
	if (!event)
		return;

	/* Every ABI byte is overwritten below, so clearing only adds stores. */
	profiler_copy_event_base(&event->base, &state->base);
	event->base.value = (__s64)duration;
	event->kind = kind;
	event->pad0 = 0;

	if (bpf_perf_event_output(ctx, &profiler_output_a,
				  COMPAT_BPF_F_CURRENT_CPU, event,
				  sizeof(*event)) < 0)
		offcpu_stat_inc(PROFILER_OFFCPU_STAT_OUTPUT_FAILURE);
}

static __always_inline int
offcpu_handle_wakeup(struct bpf_raw_tracepoint_args *ctx,
		     struct task_struct *task)
{
	struct offcpu_state *state;
	__u64 key;
	__u64 now;

	key = (__u64)task;
	if (!key)
		return 0;

	state = bpf_map_lookup_elem(&offcpu_states, &key);
	if (!state || state->kind != PROFILER_OFFCPU_EVENT_BLOCKED)
		return 0;

	now = bpf_ktime_get_ns();
	offcpu_emit_event(ctx, state, now, PROFILER_OFFCPU_EVENT_BLOCKED);

	if (profiler_offcpu_phase == OFFCPU_PHASE_FILTER_BLOCKED) {
		bpf_map_delete_elem(&offcpu_states, &key);
		return 0;
	}

	/* Preserve the captured stack and begin measuring scheduler delay. */
	state->kind = PROFILER_OFFCPU_EVENT_RUNQUEUE;
	state->phase_start_ns = now;
	return 0;
}

SEC("raw_tracepoint/sched_wakeup")
int native_offcpu_wakeup(struct bpf_raw_tracepoint_args *ctx)
{
	return offcpu_handle_wakeup(ctx, (void *)ctx->args[0]);
}

SEC("raw_tracepoint/sched_wakeup_new")
int native_offcpu_wakeup_new(struct bpf_raw_tracepoint_args *ctx)
{
	return offcpu_handle_wakeup(ctx, (void *)ctx->args[0]);
}

static __always_inline void
offcpu_record_sched_out(struct bpf_raw_tracepoint_args *ctx,
			struct task_struct *prev, __u64 now)
{
	struct offcpu_state state = {};
	bool is_runnable;
	__u64 pid_tgid;
	bool preempted;
	__u64 key;
	__u32 cpu;
	int err;

	pid_tgid = bpf_get_current_pid_tgid();
	key = (__u64)prev;

	if (!key || pid_tgid == 0 ||
	    !profiler_should_trace(pid_tgid, current_task_cpu_css_addr()))
		return;

	/* Unrelated context switches must not pay for the bitmap lookup. */
	cpu = bpf_get_smp_processor_id();
	if (!offcpu_cpu_selected(cpu))
		return;

	preempted = (__u64)ctx->args[0] != 0;
	is_runnable = preempted || task_state(prev) == TASK_RUNNING;
	if (is_runnable && profiler_offcpu_phase == OFFCPU_PHASE_FILTER_BLOCKED)
		return;

	err = profiler_fill_event_base(&state.base, pid_tgid, ctx,
				       &stack_map_a);
	if (err < 0) {
		offcpu_stat_inc(PROFILER_OFFCPU_STAT_STACK_FAILURE);
		return;
	}

	state.phase_start_ns = now;
	if (is_runnable) {
		state.kind = PROFILER_OFFCPU_EVENT_RUNQUEUE_YIELDED;
		if (preempted)
			state.kind = PROFILER_OFFCPU_EVENT_RUNQUEUE_PREEMPTED;
	} else {
		state.kind = PROFILER_OFFCPU_EVENT_BLOCKED;
	}

	if (bpf_map_update_elem(&offcpu_states, &key, &state, BPF_ANY) < 0) {
		offcpu_stat_inc(PROFILER_OFFCPU_STAT_STATE_UPDATE_FAILURE);
		return;
	}
}

static __always_inline void
offcpu_finish_sched_in(struct bpf_raw_tracepoint_args *ctx,
		       struct task_struct *next, __u64 now)
{
	enum profiler_offcpu_event_kind kind;
	struct offcpu_state *state;
	__u64 key;

	key = (__u64)next;
	if (!key)
		return;

	/* The state lookup also filters idle tasks, which are never tracked. */
	state = bpf_map_lookup_elem(&offcpu_states, &key);
	if (!state)
		return;

	kind = state->kind;
	if (kind == PROFILER_OFFCPU_EVENT_BLOCKED) {
		/* A missed wakeup makes the blocked/runqueue boundary
		 * unknowable. Report the interval separately instead of
		 * misattributing it.
		 */
		kind = PROFILER_OFFCPU_EVENT_RUNQUEUE_MISSED_WAKEUP;
		offcpu_stat_inc(PROFILER_OFFCPU_STAT_MISSED_WAKEUP);
	}

	offcpu_emit_event(ctx, state, now, kind);
	bpf_map_delete_elem(&offcpu_states, &key);
}

SEC("raw_tracepoint/sched_switch")
int native_offcpu_switch(struct bpf_raw_tracepoint_args *ctx)
{
	struct task_struct *prev = (void *)ctx->args[1];
	struct task_struct *next = (void *)ctx->args[2];
	__u64 now = bpf_ktime_get_ns();

	/* Complete next first; sched_switch guarantees prev != next. */
	offcpu_finish_sched_in(ctx, next, now);
	offcpu_record_sched_out(ctx, prev, now);
	return 0;
}

static __always_inline int
offcpu_cleanup_task_state(struct bpf_raw_tracepoint_args *ctx,
			  struct task_struct *task, bool emit_pending)
{
	struct offcpu_state *state;
	__u64 key;
	__u64 now;

	key = (__u64)task;
	if (!key)
		return 0;

	state = bpf_map_lookup_elem(&offcpu_states, &key);
	if (!state)
		return 0;

	/* sched_process_exit is the last reliable pending-sample boundary. */
	if (emit_pending) {
		now = bpf_ktime_get_ns();
		offcpu_emit_event(ctx, state, now, state->kind);
	}

	bpf_map_delete_elem(&offcpu_states, &key);
	offcpu_stat_inc(PROFILER_OFFCPU_STAT_STATE_CLEANUP);
	return 0;
}

SEC("raw_tracepoint/sched_process_exit")
int native_offcpu_exit(struct bpf_raw_tracepoint_args *ctx)
{
	return offcpu_cleanup_task_state(ctx, (void *)ctx->args[0], true);
}

SEC("raw_tracepoint/sched_process_free")
int native_offcpu_free(struct bpf_raw_tracepoint_args *ctx)
{
	/* Free can lag exit, so use it only for stale-state cleanup. */
	return offcpu_cleanup_task_state(ctx, (void *)ctx->args[0], false);
}

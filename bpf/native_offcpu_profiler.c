#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_dbg.h"
#include "bpf_map.h"
#include "bpf_profiler.h"
#include "bpf_sched.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define TASK_RUNNING	     0
#define OFFCPU_STATE_ENTRIES 32768
#define OFFCPU_CPU_SET_WORDS 128

enum offcpu_phase_filter {
	OFFCPU_PHASE_FILTER_ALL = 0,
	OFFCPU_PHASE_FILTER_BLOCKED,
	OFFCPU_PHASE_FILTER_RUNQUEUE,
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

enum offcpu_state_phase {
	OFFCPU_STATE_BLOCKED = 1,
	OFFCPU_STATE_RUNNABLE,
};

enum offcpu_stat {
	OFFCPU_STAT_STACK_ERROR = 0,
	OFFCPU_STAT_STATE_ERROR,
	OFFCPU_STAT_OUTPUT_ERROR,
	OFFCPU_STAT_MISSED_WAKEUP,
	OFFCPU_STAT_EXIT_CLEANUP,
	OFFCPU_STAT_COUNT,
};

static volatile const __u32 profiler_offcpu_phase = OFFCPU_PHASE_FILTER_ALL;
static volatile const __u64 profiler_offcpu_min_duration_ns = 1000000;
static volatile const __u32 profiler_offcpu_cpu_set_enabled = 0;
static volatile const __u32 profiler_offcpu_stats_enabled = 0;

BPF_DBG_MAP(native_cpu_dbg);

struct offcpu_state {
	struct profiler_event_base base;
	__u64 phase_start_ns;
	__u32 cpu;
	__u8 phase;
	__u8 flags;
	__u8 pad0[2];
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
	__uint(max_entries, OFFCPU_STATE_ENTRIES);
} offcpu_states SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__type(key, __u32);
	__type(value, __u64);
	__uint(max_entries, OFFCPU_STAT_COUNT);
} offcpu_stats SEC(".maps");

static __always_inline void offcpu_stat_inc(__u32 stat)
{
	__u64 *value;

	if (!profiler_offcpu_stats_enabled)
		return;

	value = bpf_map_lookup_elem(&offcpu_stats, &stat);
	if (value)
		(*value)++;
}

static __always_inline bool offcpu_event_enabled(__u8 kind)
{
	if (profiler_offcpu_phase == OFFCPU_PHASE_FILTER_ALL)
		return true;
	if (profiler_offcpu_phase == OFFCPU_PHASE_FILTER_BLOCKED)
		return kind == OFFCPU_EVENT_BLOCKED;
	return kind == OFFCPU_EVENT_RUNQUEUE;
}

static __always_inline bool offcpu_cpu_selected(__u32 cpu)
{
	__u64 *mask;
	__u32 word;

	/* Keep the default path map-lookup free after constant rewriting. */
	if (!profiler_offcpu_cpu_set_enabled)
		return true;

	word = cpu >> 6;
	if (word >= OFFCPU_CPU_SET_WORDS)
		return false;

	mask = bpf_map_lookup_elem(&offcpu_cpu_set, &word);
	return mask && (*mask & (1ULL << (cpu & 63)));
}

static __always_inline void
offcpu_emit_event(void *ctx, const struct offcpu_state *state, __u64 end_ns,
		  __u8 kind, __u8 extra_flags)
{
	struct profiler_offcpu_event *event;
	__u64 duration;
	__u32 zero = 0;
	long err;

	if (!offcpu_event_enabled(kind) || end_ns <= state->phase_start_ns)
		return;

	duration = end_ns - state->phase_start_ns;
	if (duration < profiler_offcpu_min_duration_ns)
		return;

	event = bpf_map_lookup_elem(&offcpu_event_buf, &zero);
	if (!event)
		return;

	/* Every ABI byte is overwritten below; clearing adds eight stores. */
	profiler_copy_event_base(&event->base, &state->base);
	event->base.value = (__s64)duration;
	event->start_ns = state->phase_start_ns;
	event->end_ns = end_ns;
	event->cpu = state->cpu;
	event->kind = kind;
	event->flags = state->flags | extra_flags;

	err = bpf_perf_event_output(ctx, &profiler_output_a,
				    COMPAT_BPF_F_CURRENT_CPU, event,
				    sizeof(*event));
	if (err < 0) {
		offcpu_stat_inc(OFFCPU_STAT_OUTPUT_ERROR);
		return;
	}
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
	if (!state || state->phase != OFFCPU_STATE_BLOCKED)
		return 0;

	now = bpf_ktime_get_ns();
	offcpu_emit_event(ctx, state, now, OFFCPU_EVENT_BLOCKED, 0);

	if (profiler_offcpu_phase == OFFCPU_PHASE_FILTER_BLOCKED) {
		bpf_map_delete_elem(&offcpu_states, &key);
		return 0;
	}

	/* Preserve the captured stack and begin measuring scheduler delay. */
	state->phase = OFFCPU_STATE_RUNNABLE;
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
	__u64 pid_tgid;
	__u64 key;
	__u32 cpu;
	bool is_runnable;
	bool preempted;
	long prev_state;

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
	prev_state = task_state(prev);
	is_runnable = preempted || prev_state == TASK_RUNNING;
	if (is_runnable && profiler_offcpu_phase == OFFCPU_PHASE_FILTER_BLOCKED)
		return;

	if (profiler_fill_event_base(&state.base, pid_tgid, ctx, &stack_map_a) <
	    0) {
		offcpu_stat_inc(OFFCPU_STAT_STACK_ERROR);
		return;
	}

	state.phase_start_ns = now;
	state.cpu = cpu;
	if (is_runnable) {
		state.phase = OFFCPU_STATE_RUNNABLE;
		state.flags = preempted ? OFFCPU_FLAG_PREEMPTED
					: OFFCPU_FLAG_YIELDED;
	} else {
		state.phase = OFFCPU_STATE_BLOCKED;
	}

	if (bpf_map_update_elem(&offcpu_states, &key, &state, BPF_ANY) < 0) {
		offcpu_stat_inc(OFFCPU_STAT_STATE_ERROR);
		return;
	}
}

static __always_inline void
offcpu_finish_sched_in(struct bpf_raw_tracepoint_args *ctx,
		       struct task_struct *next, __u64 now)
{
	struct offcpu_state *state;
	__u8 event_flags = 0;
	__u64 key;

	key = (__u64)next;
	if (!key)
		return;

	/*
	 * Idle tasks are never tracked, so the state lookup filters them too.
	 */
	state = bpf_map_lookup_elem(&offcpu_states, &key);
	if (!state)
		return;

	if (state->phase != OFFCPU_STATE_RUNNABLE) {
		/*
		 * The wakeup boundary is unknowable once its event is missed.
		 * Most production tasks spend less time blocked than runnable,
		 * so avoid charging the whole interval to blocking and keep the
		 * uncertainty explicit for userspace.
		 */
		event_flags = OFFCPU_FLAG_MISSED_WAKEUP;
	}
	offcpu_emit_event(ctx, state, now, OFFCPU_EVENT_RUNQUEUE, event_flags);
	if (event_flags)
		offcpu_stat_inc(OFFCPU_STAT_MISSED_WAKEUP);
	bpf_map_delete_elem(&offcpu_states, &key);
}

SEC("raw_tracepoint/sched_switch")
int native_offcpu_switch(struct bpf_raw_tracepoint_args *ctx)
{
	struct task_struct *prev = (void *)ctx->args[1];
	struct task_struct *next = (void *)ctx->args[2];
	__u64 now = bpf_ktime_get_ns();

	/*
	 * Complete next before recording prev; sched_switch guarantees they
	 * differ.
	 */
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
	__u8 event_kind;

	key = (__u64)task;
	if (!key)
		return 0;

	state = bpf_map_lookup_elem(&offcpu_states, &key);
	if (!state)
		return 0;

	/*
	 * sched_process_exit is the last reliable boundary for a pending
	 * sample.
	 */
	if (emit_pending) {
		now = bpf_ktime_get_ns();
		event_kind = state->phase == OFFCPU_STATE_RUNNABLE
				     ? OFFCPU_EVENT_RUNQUEUE
				     : OFFCPU_EVENT_BLOCKED;
		offcpu_emit_event(ctx, state, now, event_kind, 0);
	}

	bpf_map_delete_elem(&offcpu_states, &key);
	offcpu_stat_inc(OFFCPU_STAT_EXIT_CLEANUP);
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
	/*
	 * Free can lag exit, so use it only as a stale-state cleanup fallback.
	 */
	return offcpu_cleanup_task_state(ctx, (void *)ctx->args[0], false);
}

#include "vmlinux.h"

#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "abi/sched_tick_types.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define DEFAULT_SCHED_TICK_INTERVAL_THRESHOLD_NS 5000000UL
#define SCHED_TICK_REPORT_WINDOW_NS 1000000000ULL
#define MAX_SCHED_TICK_REPORTS_PER_CPU_PER_SECOND 10

volatile const u64 sched_tick_interval_threshold_ns =
	DEFAULT_SCHED_TICK_INTERVAL_THRESHOLD_NS;

struct sched_tick_state {
	u64 last_tick_ns;
	u64 report_window_start_ns;
	u32 reports_in_window;
	u32 tick_stopped;
};

static __always_inline bool
try_reserve_sched_tick_report(struct sched_tick_state *state, u64 now)
{
	if (!state->report_window_start_ns ||
	    now - state->report_window_start_ns >= SCHED_TICK_REPORT_WINDOW_NS) {
		state->report_window_start_ns = now;
		state->reports_in_window = 0;
	}

	if (state->reports_in_window >=
	    MAX_SCHED_TICK_REPORTS_PER_CPU_PER_SECOND)
		return false;

	state->reports_in_window++;
	return true;
}

/*
 * Keep timing state per CPU because scheduler ticks and NO_HZ state are local.
 */
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(struct sched_tick_state));
	__uint(max_entries, 1);
} sched_tick_states SEC(".maps");

/* Avoid allocating the large stack payload on the BPF program stack. */
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(u32)); /* key = 0 */
	__uint(value_size, sizeof(struct sched_tick_event));
	__uint(max_entries, 1);
} sched_tick_event_buf SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} sched_tick_events SEC(".maps");

SEC("kprobe/account_process_tick")
void trace_sched_tick_interval(struct pt_regs *ctx)
{
	u32 key = 0;
	struct sched_tick_state *state;
	struct sched_tick_event *event;
	u64 now;
	u64 tick_interval;

	state = bpf_map_lookup_elem(&sched_tick_states, &key);
	if (!state || state->tick_stopped)
		return;

	now = bpf_ktime_get_ns();
	if (!state->last_tick_ns) {
		state->last_tick_ns = now;
		return;
	}

	tick_interval = now - state->last_tick_ns;
	state->last_tick_ns = now;
	if (tick_interval < sched_tick_interval_threshold_ns ||
	    !try_reserve_sched_tick_report(state, now))
		return;

	event = bpf_map_lookup_elem(&sched_tick_event_buf, &key);
	if (!event)
		return;

	event->timestamp_ns = now;
	event->tick_interval_ns = tick_interval;
	if (bpf_get_current_comm(event->comm, sizeof(event->comm)))
		return;

	event->tgid = (u32)(bpf_get_current_pid_tgid() >> 32);
	event->cpu = bpf_get_smp_processor_id();
	event->stack_size =
	    bpf_get_stack(ctx, event->stack, sizeof(event->stack), 0);

	bpf_perf_event_output(ctx, &sched_tick_events,
			      COMPAT_BPF_F_CURRENT_CPU, event,
			      sizeof(struct sched_tick_event));
}

SEC("tracepoint/timer/tick_stop")
void trace_sched_tick_stop(struct trace_event_raw_tick_stop *ctx)
{
	struct sched_tick_state *state;
	u32 key = 0;

	state = bpf_map_lookup_elem(&sched_tick_states, &key);
	if (!state)
		return;

	/* Ignore failed tick-stop attempts. */
	if (ctx->success) {
		state->tick_stopped = 1;
		state->last_tick_ns = 0;
	}
}

SEC("kprobe/tick_nohz_restart_sched_tick")
void trace_sched_tick_restart(struct pt_regs *ctx)
{
	struct sched_tick_state *state;
	u32 key = 0;

	state = bpf_map_lookup_elem(&sched_tick_states, &key);
	if (!state)
		return;

	state->last_tick_ns = bpf_ktime_get_ns();
	state->tick_stopped = 0;
}

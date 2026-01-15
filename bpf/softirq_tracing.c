#include "vmlinux.h"

#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_ratelimit.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define NR_STACK_TRACE_MAX 0x4000
#define MSEC_PER_NSEC 1000000UL
#define TICK_DEP_MASK_NONE 0
#define SOFTIRQ_THRESH 5000000UL

volatile const u64 softirq_thresh = SOFTIRQ_THRESH;
bool idle_polling = true;

#define TICK 1000
BPF_RATELIMIT(rate, 1, COMPAT_CPU_NUM *TICK * 1000);

struct timer_softirq_run_ts {
	u32 start_trace;
	u32 restarting_tick;
	u64 soft_ts;
};

struct report_event {
	u64 stack[PERF_MAX_STACK_DEPTH];
	s64 stack_size;
	u64 now;
	u64 stall_time;
	char comm[COMPAT_TASK_COMM_LEN];
	u32 pid;
	u32 cpu;
};

// the map for recording irq/softirq timer ts
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(struct timer_softirq_run_ts));
	__uint(max_entries, 1);
} timerts_map SEC(".maps");

// the map use for storing struct report_event memory
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(u32)); // key = 0
	__uint(value_size, sizeof(struct report_event));
	__uint(max_entries, 1);
} report_map SEC(".maps");

// the event map use for report userspace
struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} irqoff_event_map SEC(".maps");

SEC("kprobe/account_process_tick")
void probe_account_process_tick(struct pt_regs *ctx)
{
	// verify bpf-ratelimit
	if (bpf_ratelimited(&rate))
		return;

	// update soft timer timestamps
	int key = 0;
	struct timer_softirq_run_ts *ts;
	// struct thresh_data *tdata;
	struct report_event *event;
	u64 now;
	u64 delta;

	ts = bpf_map_lookup_elem(&timerts_map, &key);
	if (!ts)
		return;

	if (!ts->start_trace && !idle_polling)
		return;

	// update soft timer timestamps
	if (!ts->soft_ts) {
		ts->soft_ts = bpf_ktime_get_ns();
		return;
	}

	event = bpf_map_lookup_elem(&report_map, &key);
	if (!event)
		return;

	if (ts->restarting_tick) {
		ts->restarting_tick = 0;
		ts->soft_ts	    = bpf_ktime_get_ns();

		return;
	}

	now   = bpf_ktime_get_ns();
	delta = now - ts->soft_ts;

	// if delta over threshold, dump important info to user
	if (delta >= softirq_thresh) {
		event->now	  = now;
		event->stall_time = delta;
		__builtin_memset(event->comm, 0, sizeof(event->comm));
		bpf_get_current_comm(&event->comm, sizeof(event->comm));
		event->pid = (u32)bpf_get_current_pid_tgid();
		event->cpu = bpf_get_smp_processor_id();
		event->stack_size =
		    bpf_get_stack(ctx, event->stack, sizeof(event->stack), 0);

		bpf_perf_event_output(ctx, &irqoff_event_map,
				      COMPAT_BPF_F_CURRENT_CPU, event,
				      sizeof(struct report_event));
	}

	// update soft_ts, use for next trace
	ts->soft_ts = now;
}

SEC("tracepoint/timer/tick_stop")
void probe_tick_stop(struct trace_event_raw_tick_stop *ctx)
{
	struct timer_softirq_run_ts *ts;
	int key = 0;

	if (idle_polling)
		idle_polling = false;

	ts = bpf_map_lookup_elem(&timerts_map, &key);
	if (!ts)
		return;

	if (ctx->success == 1 && ctx->dependency == TICK_DEP_MASK_NONE) {
		ts->start_trace = 0;
	}

	return;
}

SEC("kprobe/tick_nohz_restart_sched_tick")
void probe_tick_nohz_restart_sched_tick(struct pt_regs *ctx)
{
	struct timer_softirq_run_ts *ts;
	int key = 0;
	u64 now;

	ts = bpf_map_lookup_elem(&timerts_map, &key);
	if (!ts)
		return;

	now = bpf_ktime_get_ns();

	ts->soft_ts	    = now;
	ts->start_trace	    = 1;
	ts->restarting_tick = 1;
}

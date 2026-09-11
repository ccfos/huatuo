#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_ratelimit.h"

char __license[] SEC("license") = "Dual MIT/GPL";

// target_cpu is injected by userspace via const-rewrite on each trigger.
volatile const u32 target_cpu = 0;

BPF_RATELIMIT_IN_MAP(source_rate, BPF_NSEC_PER_SEC, 500, 0);
BPF_RATELIMIT_IN_MAP(victim_rate, BPF_NSEC_PER_SEC, 500, 0);

struct stack_key {
	u32 ustack_id;
	u32 kstack_id;
	u32 pid;
	u32 vec;
	char comm[COMPAT_TASK_COMM_LEN];
};

// rq_task records a runqueue member of the target cpu (key = pid).
struct rq_task {
	char comm[COMPAT_TASK_COMM_LEN];
};

// rq_tasks tracks the runqueue members of the target cpu (key = pid).
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(struct rq_task));
	__uint(max_entries, 4096);
} rq_tasks SEC(".maps");

// source_counts aggregates the stacks that raised softirqs on the target cpu.
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(key_size, sizeof(struct stack_key));
	__uint(value_size, sizeof(u64));
	__uint(max_entries, 1024);
} source_counts SEC(".maps");

// victim_counts aggregates stacks of tasks preempted by softirq (non-ksoftirqd)
// on the target cpu. Full kstack/ustack is captured.
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(key_size, sizeof(struct stack_key));
	__uint(value_size, sizeof(u64));
	__uint(max_entries, 1024);
} victim_counts SEC(".maps");

// dropped_samples counts the samples discarded during collection (by the
// first-N budget or by a full counts map; 0: source, 1: victim) so userspace
// can mark the result incomplete. The bpf_rlimit_* state maps are internal to
// the rate limiter and not readable by userspace, so the drop count must
// live in an ordinary map of our own.
struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(u64));
	__uint(max_entries, 2);
} dropped_samples SEC(".maps");

// rq_victim_window records the last softirq serviced by ksoftirqd on the
// target cpu. It marks a window during which every task currently queued on
// rq_tasks is a victim waiting behind the softirq. We cannot capture those
// tasks' backtraces from this context, so userspace reads the rq_tasks
// members together with this entry and records them as victims with an
// [unknown] stack.
struct rq_victim_window {
	u64 ts;
	u32 cpu;
	u32 vec;
};

// ksoftirqd_window is a one-entry state map instead of a perf stream: the
// probe only needs to communicate "ksoftirqd ran" plus the last vector, and
// userspace reads the entry after detaching the probes, so there is no
// drain/flush contract and no events can be lost between reader shutdown
// and detach.
struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(struct rq_victim_window));
	__uint(max_entries, 1);
} ksoftirqd_window SEC(".maps");

// stack_traces stores the raw kernel/user stack frames; stack_key only keeps
// the 32-bit stack ids returned by bpf_get_stackid. max_entries is sized for
// the short collection window and deduplicated unique stacks, which is far
// below this limit.
struct {
	__uint(type, BPF_MAP_TYPE_STACK_TRACE);
	__uint(key_size, sizeof(u32));
	__uint(value_size, PERF_MAX_STACK_DEPTH * sizeof(u64));
	__uint(max_entries, 16384);
} stack_traces SEC(".maps");

// PF_KTHREAD marks a kernel thread (include/linux/sched.h). It is a stable
// kernel ABI constant that we define locally to avoid depending on userspace
// headers. Used to disambiguate the ksoftirqd kernel thread from a userspace
// process that renamed itself (e.g. via prctl(PR_SET_NAME)) to look like it.
#define PF_KTHREAD 0x00200000

static __always_inline int is_ksoftirqd(const char *comm, u32 flags)
{
	// "ksoftirqd" is 9 chars; must also be an actual kernel thread.
	return (flags & PF_KTHREAD) &&
	       comm[0] == 'k' && comm[1] == 's' && comm[2] == 'o' &&
	       comm[3] == 'f' && comm[4] == 't' && comm[5] == 'i' &&
	       comm[6] == 'r' && comm[7] == 'q' && comm[8] == 'd';
}

// count_drop records a discarded sample of the given stream in
// dropped_samples.
static __always_inline void count_drop(u32 stream)
{
	u64 *cnt = bpf_map_lookup_elem(&dropped_samples, &stream);

	if (cnt)
		__sync_fetch_and_add(cnt, 1);
}

// NO_STACK_ID marks a stack capture that failed inside the kernel: the probe
// rewrites a negative bpf_get_stackid errno into this sentinel so userspace
// can tell "no stack" apart from a valid stack id, which starts at 0. The
// value cannot collide with a real id because ids index the stack_traces map
// (0..max_entries-1), far below U32_MAX.
#define NO_STACK_ID 0xFFFFFFFF

// account_stack aggregates the stack of the current event into map. stream
// (0: source, 1: victim) selects the drop counter used when the map is full
// and the sample cannot be recorded.
static __always_inline void account_stack(void *map, struct pt_regs *ctx,
					  u32 vec, u32 stream)
{
	struct stack_key key = {};
	u64 *valp;
	u64 cnt = 1;
	long ustack_id;
	long kstack_id;

	key.pid = (u32)(bpf_get_current_pid_tgid() >> 32);
	key.vec = vec;
	bpf_get_current_comm(&key.comm, sizeof(key.comm));

	ustack_id = bpf_get_stackid(ctx, &stack_traces, COMPAT_BPF_F_USER_STACK);
	kstack_id = bpf_get_stackid(ctx, &stack_traces, 0);
	key.ustack_id = ustack_id < 0 ? NO_STACK_ID : (u32)ustack_id;
	key.kstack_id = kstack_id < 0 ? NO_STACK_ID : (u32)kstack_id;

	valp = bpf_map_lookup_elem(map, &key);
	if (!valp) {
		// The counts maps have a fixed capacity: a full map loses the
		// sample even though it passed the rate limiter, so record it in
		// dropped_samples or nmissed would claim a complete profile.
		if (bpf_map_update_elem(map, &key, &cnt, COMPAT_BPF_ANY) != 0)
			count_drop(stream);
		return;
	}

	__sync_fetch_and_add(valp, 1);
}

SEC("kprobe/activate_task")
void probe_activate_task(struct pt_regs *ctx)
{
	struct rq *rq = (struct rq *)PT_REGS_PARM1(ctx);
	struct task_struct *p = (struct task_struct *)PT_REGS_PARM2(ctx);
	struct rq_task task = {};
	u32 pid;

	if ((u32)BPF_CORE_READ(rq, cpu) != target_cpu)
		return;

	bpf_probe_read_str(task.comm, sizeof(task.comm),
			   BPF_CORE_READ(p, comm));
	if (is_ksoftirqd(task.comm, (u32)BPF_CORE_READ(p, flags)))
		return;

	pid = (u32)BPF_CORE_READ(p, pid);
	bpf_map_update_elem(&rq_tasks, &pid, &task, COMPAT_BPF_ANY);
}

SEC("kprobe/deactivate_task")
void probe_deactivate_task(struct pt_regs *ctx)
{
	struct rq *rq = (struct rq *)PT_REGS_PARM1(ctx);
	struct task_struct *p = (struct task_struct *)PT_REGS_PARM2(ctx);
	u32 pid;

	if ((u32)BPF_CORE_READ(rq, cpu) != target_cpu)
		return;

	pid = (u32)BPF_CORE_READ(p, pid);
	bpf_map_delete_elem(&rq_tasks, &pid);
}

SEC("tracepoint/irq/softirq_raise")
int probe_softirq_raise(struct trace_event_raw_softirq *ctx)
{
	if (bpf_get_smp_processor_id() != target_cpu)
		return 0;

	if (bpf_ratelimited_in_map(ctx, source_rate)) {
		count_drop(0);
		return 0;
	}

	account_stack(&source_counts, (struct pt_regs *)ctx, ctx->vec, 0);
	return 0;
}

SEC("tracepoint/irq/softirq_entry")
int probe_softirq_entry(struct trace_event_raw_softirq *ctx)
{
	char comm[COMPAT_TASK_COMM_LEN] = {};
	struct rq_victim_window win;
	struct task_struct *cur;
	u32 flags;
	u32 pid;
	u32 zero = 0;

	if (bpf_get_smp_processor_id() != target_cpu)
		return 0;

	bpf_get_current_comm(&comm, sizeof(comm));
	cur = (struct task_struct *)bpf_get_current_task();
	flags = (u32)BPF_CORE_READ(cur, flags);

	// When the current task is ksoftirqd, the softirq is not preempting a
	// real task; the actual victims are the tasks queued on the runqueue
	// (tracked in rq_tasks). Record the window in ksoftirqd_window so
	// userspace can read those members and merge them as victims. Otherwise
	// the current task is the one being preempted — record its stack
	// directly as a victim.
	if (is_ksoftirqd(comm, flags)) {
		win.ts = bpf_ktime_get_ns();
		win.cpu = bpf_get_smp_processor_id();
		win.vec = ctx->vec;
		bpf_map_update_elem(&ksoftirqd_window, &zero, &win,
				    COMPAT_BPF_ANY);
		return 0;
	}

	// The idle task (swapper/N, pid == 0) is not a real victim: interrupting
	// it has no impact on running business, so skip it.
	pid = (u32)(bpf_get_current_pid_tgid() >> 32);
	if (pid == 0)
		return 0;

	// Limit only the expensive stack capture: ksoftirqd window events and
	// idle entries above must never consume the victim budget, otherwise a
	// burst of them could starve real victims.
	if (bpf_ratelimited_in_map(ctx, victim_rate)) {
		count_drop(1);
		return 0;
	}

	account_stack(&victim_counts, (struct pt_regs *)ctx, ctx->vec, 1);
	return 0;
}

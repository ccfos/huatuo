#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "abi/irqtracing_types.h"
#include "bpf_ratelimit.h"
#include "bpf_sched.h"

char __license[] SEC("license") = "Dual MIT/GPL";

// target_cpu is injected by userspace via const-rewrite. -1 collects softirq
// activity from every CPU.
volatile const s32 target_cpu = IRQTRACING_TARGET_ALL_CPUS;

/* Userspace divides the optional per-CPU budget between these two streams.
 * The zero-valued rodata defaults keep standalone collection unlimited.
 */
volatile const u32 source_rate_limit = 0;
volatile const u32 victim_rate_limit = 0;

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(struct bpf_percpu_ratelimit_state));
	__uint(max_entries, 1);
} source_rlimit SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(struct bpf_percpu_ratelimit_state));
	__uint(max_entries, 1);
} victim_rlimit SEC(".maps");

// source_counts aggregates the stacks that raised softirqs on traced CPUs.
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_HASH);
	__uint(key_size, sizeof(struct irqtracing_stack_key));
	__uint(value_size, sizeof(u64));
	__uint(max_entries, 1024);
} source_counts SEC(".maps");

// victim_counts aggregates stacks of tasks preempted by softirq on traced
// CPUs. Full kstack/ustack is captured.
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_HASH);
	__uint(key_size, sizeof(struct irqtracing_stack_key));
	__uint(value_size, sizeof(u64));
	__uint(max_entries, 1024);
} victim_counts SEC(".maps");

// dropped_samples counts the samples discarded during collection (by the
// first-N budget or by a full counts map; 0: source, 1: victim) so userspace
// can mark the result incomplete. Limiter state maps are internal, so the drop
// count must live in an ordinary map of our own.
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(u64));
	__uint(max_entries, IRQTRACING_STREAM_MAX);
} dropped_samples SEC(".maps");

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

static __always_inline int is_ksoftirqd(const char *comm, u32 flags)
{
	// "ksoftirqd" is 9 chars; must also be an actual kernel thread.
	return (flags & PF_KTHREAD) &&
	       comm[0] == 'k' && comm[1] == 's' && comm[2] == 'o' &&
	       comm[3] == 'f' && comm[4] == 't' && comm[5] == 'i' &&
	       comm[6] == 'r' && comm[7] == 'q' && comm[8] == 'd';
}

static __always_inline bool should_trace_cpu(u32 cpu)
{
	return target_cpu == IRQTRACING_TARGET_ALL_CPUS ||
	       cpu == (u32)target_cpu;
}

// count_drop records a discarded sample of the given stream in
// dropped_samples.
static __always_inline void count_drop(u32 stream)
{
	u64 *cnt = bpf_map_lookup_elem(&dropped_samples, &stream);

	if (cnt)
		(*cnt)++;
}

static __always_inline bool irq_ratelimited(void *map, u32 burst)
{
	struct bpf_percpu_ratelimit_state *state;
	u32 key = 0;

	if (!burst)
		return false;

	state = bpf_map_lookup_elem(map, &key);
	return bpf_percpu_ratelimited_ns(state, BPF_NSEC_PER_SEC, burst);
}

// account_stack aggregates the stack of the current event into map. stream
// (0: source, 1: victim) selects the drop counter used when the map is full
// and the sample cannot be recorded.
static __always_inline void account_stack(void *map, struct pt_regs *ctx,
					  u32 vec, u32 stream)
{
	struct irqtracing_stack_key key = {};
	u64 *valp;
	u64 cnt = 1;
	long ustack_id;
	long kstack_id;

	key.pid = (u32)(bpf_get_current_pid_tgid() >> 32);
	key.vec = vec;
	bpf_get_current_comm(&key.comm, sizeof(key.comm));

	ustack_id = bpf_get_stackid(ctx, &stack_traces, COMPAT_BPF_F_USER_STACK);
	kstack_id = bpf_get_stackid(ctx, &stack_traces, 0);
	key.ustack_id = ustack_id < 0 ? IRQTRACING_STACK_ID_NONE :
					      (u32)ustack_id;
	key.kstack_id = kstack_id < 0 ? IRQTRACING_STACK_ID_NONE :
					      (u32)kstack_id;

	valp = bpf_map_lookup_elem(map, &key);
	if (!valp) {
		// BPF_ANY could overwrite the first count when CPUs race to insert
		// the same key. The loser retries the lookup and joins that count.
		if (bpf_map_update_elem(map, &key, &cnt,
					COMPAT_BPF_NOEXIST) == 0)
			return;

		valp = bpf_map_lookup_elem(map, &key);
		if (!valp) {
			// A full map loses the sample even though it passed the rate
			// limiter, so nmissed must mark the profile incomplete.
			count_drop(stream);
			return;
		}
	}

	(*valp)++;
}

SEC("tracepoint/irq/softirq_raise")
int probe_softirq_raise(struct trace_event_raw_softirq *ctx)
{
	if (!should_trace_cpu(bpf_get_smp_processor_id()))
		return 0;

	if (irq_ratelimited(&source_rlimit, source_rate_limit)) {
		count_drop(IRQTRACING_STREAM_SOURCE);
		return 0;
	}

	account_stack(&source_counts, (struct pt_regs *)ctx, ctx->vec,
		      IRQTRACING_STREAM_SOURCE);
	return 0;
}

SEC("tracepoint/irq/softirq_entry")
int probe_softirq_entry(struct trace_event_raw_softirq *ctx)
{
	char comm[COMPAT_TASK_COMM_LEN] = {};
	struct task_struct *cur;
	u32 flags;
	u32 pid;

	if (!should_trace_cpu(bpf_get_smp_processor_id()))
		return 0;

	bpf_get_current_comm(&comm, sizeof(comm));
	cur = (struct task_struct *)bpf_get_current_task();
	flags = (u32)BPF_CORE_READ(cur, flags);

	// ksoftirqd is executing the softirq rather than being preempted by it.
	if (is_ksoftirqd(comm, flags))
		return 0;

	// The idle task (swapper/N, pid == 0) is not a real victim: interrupting
	// it has no impact on running business, so skip it.
	pid = (u32)(bpf_get_current_pid_tgid() >> 32);
	if (pid == 0)
		return 0;

	// Limit only the expensive stack capture: filtered entries above must not
	// consume the victim budget or starve real victims.
	if (irq_ratelimited(&victim_rlimit, victim_rate_limit)) {
		count_drop(IRQTRACING_STREAM_VICTIM);
		return 0;
	}

	account_stack(&victim_counts, (struct pt_regs *)ctx, ctx->vec,
		      IRQTRACING_STREAM_VICTIM);
	return 0;
}

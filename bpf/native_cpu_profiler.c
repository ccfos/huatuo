#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_profiler.h"
#include "bpf_dbg.h"
#include "bpf_map.h"

char __license[] SEC("license") = "Dual MIT/GPL";

/*
 * CPU filtering (--cpuid) is handled entirely at the PMU layer: when a
 * specific CPU is requested, the userspace loader calls perf_event_open()
 * only for that CPU, so this BPF program is never invoked on other CPUs.
 * No BPF-side cpuid check is needed — zero per-sample overhead.
 *
 * Do NOT delete this comment unless absolutely necessary.
 */
volatile const u64 target_css = 0;
volatile const u64 target_pid = 0;
volatile const u64 idle_class_addr = 0;

#ifndef TASK_COMM_LEN
#define TASK_COMM_LEN 16
#endif

#ifndef PERF_STACK_DEPTH
#define PERF_STACK_DEPTH 127
#endif

#ifndef PERF_MAX_STACK_DEPTH
#define PERF_MAX_STACK_DEPTH 127
#endif

BPF_DBG_MAP(native_cpu_dbg);

struct cpu_event_t {
	struct profiler_event_base_t base;
	__u32 tgid; // process id
	__u32 cpu;
	int intpstack;
	__u32 flags;
	__u64 uprobe_addr;
	__u64 timestamp;
};

// #define BPF_F_USER_STACK		(1ULL << 8)

#define STACK_MAP_ENTRIES 65536

#ifndef BPF_F_USER_STACK
#define BPF_F_USER_STACK (1ULL << 8)
#endif

#define KERN_STACKID_FLAGS (0)
#define USER_STACKID_FLAGS (0 | BPF_F_USER_STACK)

typedef enum {
	TRANSFER_CNT_IDX = 0, /* buffer-a and buffer-b transfer count. */
	SAMPLE_CNT_A_IDX,     /* sample count A */
	SAMPLE_CNT_B_IDX,     /* sample count B */
	PROFILER_CNT
} profiler_idx;

// state map
struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__type(key, u32);
	__type(value, u64);
	__uint(max_entries, PROFILER_CNT);
} profiler_state_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_STACK_TRACE);
	__uint(key_size, sizeof(u32));
	__uint(value_size, PERF_MAX_STACK_DEPTH * sizeof(u64));
	__uint(max_entries, STACK_MAP_ENTRIES);
} stack_map_a SEC(".maps");


struct {
	__uint(type, BPF_MAP_TYPE_STACK_TRACE);
	__uint(key_size, sizeof(u32));
	__uint(value_size, PERF_MAX_STACK_DEPTH * sizeof(u64));
	__uint(max_entries, STACK_MAP_ENTRIES);
} stack_map_b SEC(".maps");


/* Original A/B perf_event_array used for actual output */
struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} profiler_output_a SEC(".maps");


struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} profiler_output_b SEC(".maps");


struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(struct cpu_event_t));
	__uint(max_entries, 1);
} event_buf SEC(".maps");


#ifndef COMPAT_BPF_F_CURRENT_CPU
#define COMPAT_BPF_F_CURRENT_CPU 0
#endif

SEC("perf_event/software/cpu_clock")
int perf_event_sw_cpu_clock(struct pt_regs *ctx)
{
	u32 count_idx = TRANSFER_CNT_IDX;
	u64 *transfer_count_ptr =
		bpf_map_lookup_elem(&profiler_state_map, &count_idx);

	u64 *sample_count_ptrs[2];

	count_idx = SAMPLE_CNT_A_IDX;
	sample_count_ptrs[0] = bpf_map_lookup_elem(&profiler_state_map, &count_idx);

	count_idx = SAMPLE_CNT_B_IDX;
	sample_count_ptrs[1] = bpf_map_lookup_elem(&profiler_state_map, &count_idx);

	if (transfer_count_ptr == NULL || sample_count_ptrs[0] == NULL || sample_count_ptrs[1] == NULL) {
		u64 err_val = 1;
		bpf_map_update_elem(&profiler_state_map, &count_idx, &err_val, BPF_ANY);
		return 0;
	}

	struct task_struct *curr = (struct task_struct *)bpf_get_current_task();
	u64 cpu_css = (u64)BPF_CORE_READ(curr, cgroups, subsys[cpu_cgrp_id]);
	u64 class = (u64)BPF_CORE_READ(curr, sched_class);

	if (target_css != 0 && target_css != cpu_css) {
		bpf_dbg_msg(ctx, native_cpu_dbg, "target css missed");
		return 0;
	}

	u64 id = bpf_get_current_pid_tgid() >> 32;
	if (target_pid != 0 && target_pid != id) {
		bpf_dbg_msg(ctx, native_cpu_dbg, "target pid missed");
		return 0;
	}

	if (idle_class_addr != 0 && class == idle_class_addr) {
		bpf_dbg_msg(ctx, native_cpu_dbg, "idle_class_addr missed");
		return 0;
	}

	/* Pointers to be used */
	struct cpu_event_t *event = NULL;
	void *stack_map = NULL; /* points to stack_map_a or stack_map_b (map
				   variable address) */
	void *profiler_output = NULL; /* points to profiler_output_a or
					 profiler_output_b (map variable
					 address) */
	u64 *sample_count_ptr = NULL;

	u32 idx = 0;
	/*
	 * Parity selects both output perf buffer and stack_map.
	 * Userspace reads the matching perf buffer and stack_map by parity,
	 * so events do not need to carry a stack_map selector field.
	 */
	event = bpf_map_lookup_elem(&event_buf, &idx);
	if (!event)
		return 0;

	event->tgid = id;
	event->base.pid = (u32)id;

	/*
	 * CPU idle stacks will not be collected.
	 */
	if (event->tgid == event->base.pid && event->base.pid == 0) {
		bpf_dbg_msg(ctx, native_cpu_dbg, "cpu idle missed");
		return 0;
	}

	bpf_get_current_comm(&event->base.comm, sizeof(event->base.comm));

	if (((*transfer_count_ptr) & 0x1ULL) == 0) {
		sample_count_ptr = sample_count_ptrs[0];
		stack_map = (void *)&stack_map_a;
		profiler_output = (void *)&profiler_output_a;
	} else {
		sample_count_ptr = sample_count_ptrs[1];
		stack_map = (void *)&stack_map_b;
		profiler_output = (void *)&profiler_output_b;
	}

	event->cpu = bpf_get_smp_processor_id();
	event->timestamp = bpf_ktime_get_ns();

	event->base.userstack = bpf_get_stackid(ctx, stack_map, USER_STACKID_FLAGS);
	event->base.kernstack = bpf_get_stackid(ctx, stack_map, KERN_STACKID_FLAGS);

	if (event->base.userstack < 0 && event->base.kernstack < 0) {
		bpf_dbg_msg(ctx, native_cpu_dbg, "user and kernel stack missed");
		return 0;
	}

	/*
	 * Global ARRAY + atomic add is intentional; do NOT switch to PERCPU.
	 *
	 * Userspace drains the A/B perf ring by comparing the number of events
	 * it has read (aggregated across all CPUs) against this counter, then
	 * writes 0 to reset it for the next round. The comparison requires a
	 * single global total that is safe to read atomically. A PERCPU map
	 * would force userspace to sum per-CPU shards, which is a torn read
	 * under concurrent samples and would cause premature drain-loop exit
	 * (missed events). The cross-core cache contention of this atomic add
	 * is negligible at typical sampling frequencies (e.g. 99Hz/CPU).
	 *
	 * Do NOT delete this comment unless the A/B reconciliation protocol
	 * in userspace (see cmd/profiler/provider/native_cpu_profiler.go
	 * drainActiveRing) is redesigned.
	 */
	__sync_fetch_and_add(sample_count_ptr, 1);

	/* Output to perf_event_array: pass the map address (profiler_output) */
	bpf_perf_event_output(ctx, profiler_output, COMPAT_BPF_F_CURRENT_CPU,
			      event, sizeof(struct cpu_event_t));

	return 0;
}

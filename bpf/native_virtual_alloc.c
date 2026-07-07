#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_core_read.h>
#include "bpf_common.h"

#define BPF_F_USER_STACK (1ULL << 8)

volatile const u64 target_css = 0;
volatile const u32 target_pid = 0;
volatile const bool trace_threads = false; // if true, match tgid, else match pid

enum {
	TRANSFER_CNT_IDX = 0,
	SAMPLE_CNT_A_IDX,
	SAMPLE_CNT_B_IDX,
	PROFILER_CNT,
};

struct mem_event_t {
	u32 pid;
	char comm[COMPAT_TASK_COMM_LEN];
	int kernstack;
	int userstack;
	/*
	 * stack_map_sel indicates which A/B stack_map the stack IDs belong to.
	 * In accumulative modes this always matches the current A/B parity (active reader).
	 * Retained mode needs this for frees, which may refer to the other map after parity flips.
	 * Kept here for a shared event layout.
	 */
	u32 stack_map_sel;
	s64 value; /* bytes for native_virtual_alloc */
};

struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__type(key, u32);
	__type(value, u64);
	__uint(max_entries, PROFILER_CNT);
} profiler_state_map SEC(".maps");

#define STACK_MAP_ENTRIES 65536

#define KERN_STACKID_FLAGS (0)
#define USER_STACKID_FLAGS (0 | BPF_F_USER_STACK)

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
	__uint(value_size, sizeof(struct mem_event_t));
	__uint(max_entries, 1);
} event_buf SEC(".maps");

#ifndef COMPAT_BPF_F_CURRENT_CPU
#define COMPAT_BPF_F_CURRENT_CPU 0
#endif

static __always_inline int should_trace(u32 current_pid, u32 current_tgid)
{
	if (target_css != 0) {
		struct task_struct *task =
			(struct task_struct *)bpf_get_current_task();
		u64 css = (u64)BPF_CORE_READ(task, cgroups, subsys[memory_cgrp_id]);
		return css == target_css;
	}
	if (target_pid == 0) {
		/* No PID filter */
		return 1;
	}
	if (trace_threads) {
		return current_tgid == target_pid;
	}
	return current_pid == target_pid;
}

SEC("kprobe/do_mmap")
int BPF_KPROBE(trace_mmap, struct file *file, unsigned long addr,
		   unsigned long len)
{
	u32 count_idx = TRANSFER_CNT_IDX;
	u64 *transfer_count_ptr =
		bpf_map_lookup_elem(&profiler_state_map, &count_idx);

	u64 *sample_count_ptrs[2];

	count_idx = SAMPLE_CNT_A_IDX;
	sample_count_ptrs[0] =
		bpf_map_lookup_elem(&profiler_state_map, &count_idx);

	count_idx = SAMPLE_CNT_B_IDX;
	sample_count_ptrs[1] =
		bpf_map_lookup_elem(&profiler_state_map, &count_idx);

	if (transfer_count_ptr == NULL || sample_count_ptrs[0] == NULL ||
		sample_count_ptrs[1] == NULL) {
		return 0;
	}

	u64 id = bpf_get_current_pid_tgid();
	u32 tgid = id >> 32;
	u32 pid = id & 0xffffffffUL;

	if (!should_trace(pid, tgid))
		return 0;

	/* Only trace anonymous mappings (no file backing) */
	if (file)
		return 0;

	struct mem_event_t *event = NULL;
	void *stack_map = NULL;
	void *profiler_output = NULL;
	u64 *sample_count_ptr = NULL;
	u32 stack_map_sel = 0;
	u32 idx = 0;

	event = bpf_map_lookup_elem(&event_buf, &idx);
	if (!event)
		return 0;

	if (((*transfer_count_ptr) & 0x1ULL) == 0) {
		profiler_output = (void *)&profiler_output_a;
		sample_count_ptr = sample_count_ptrs[0];
		stack_map = (void *)&stack_map_a;
		stack_map_sel = 0;
	} else {
		profiler_output = (void *)&profiler_output_b;
		sample_count_ptr = sample_count_ptrs[1];
		stack_map = (void *)&stack_map_b;
		stack_map_sel = 1;
	}

	if (!event)
		return 0;

	__builtin_memset(event, 0, sizeof(*event));

	event->pid = tgid;
	event->stack_map_sel = stack_map_sel;
	bpf_get_current_comm(&event->comm, sizeof(event->comm));

	event->userstack =
		bpf_get_stackid(ctx, stack_map, USER_STACKID_FLAGS);
	event->kernstack =
		bpf_get_stackid(ctx, stack_map, KERN_STACKID_FLAGS);

	if (event->userstack < 0 && event->kernstack < 0)
		return 0;

	event->value = (s64)len;

	__sync_fetch_and_add(sample_count_ptr, 1);

	bpf_perf_event_output(ctx, profiler_output, COMPAT_BPF_F_CURRENT_CPU,
				  event, sizeof(*event));

	return 0;
}

char LICENSE[] SEC("license") = "GPL";

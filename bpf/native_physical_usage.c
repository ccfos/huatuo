#include "vmlinux.h"
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include <bpf/bpf_core_read.h>
#include "bpf_profiler.h"

#define BPF_F_USER_STACK (1ULL << 8)
#define BPF_ANY 0

volatile const u64 target_css = 0;
volatile const u32 target_pid = 0;
volatile const u8 sampling_probability = 10; // 0-100, 10 means 10%
volatile const bool trace_threads = false;   // if true, match thread group, else only match pid

enum {
	TRANSFER_CNT_IDX = 0,
	SAMPLE_CNT_A_IDX,
	SAMPLE_CNT_B_IDX,
	PROFILER_CNT,
};

struct stack_info_t {
	struct profiler_event_base_t base;
	/*
	 * stack_map_sel indicates which A/B stack_map the IDs were taken from.
	 * Alloc events set this to current parity. Free events reuse the alloc-time selector
	 * stored in page_to_stackid, so it can differ from current parity after flips.
	 */
	u32 stack_map_sel;
};

/*
 * page_to_stackid tracks retained (live) anonymous pages.
 *
 * Key: page address (struct page *).
 * Value: stack_info_t captured at allocation time (PID/comm + stack IDs + stack_map_sel).
 *
 * When a page is freed, we look up the original alloc stack here so the -1 event
 * is attributed to the same stack, even if A/B parity has flipped since alloc.
 */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 1 << 20);
	__type(key, u64);
	__type(value, struct stack_info_t);
} page_to_stackid SEC(".maps");

struct mem_event_t {
	struct profiler_event_base_t base;
	/*
	 * stack_map_sel indicates which A/B stack_map the stack IDs belong to.
	 * Alloc events set this to current parity; free events reuse alloc-time selector.
	 */
	u32 stack_map_sel;
	s64 value; /* pages delta: +1 on alloc, -1 on free */
};

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
		return 1;
	}
	if (trace_threads) {
		return current_tgid == target_pid;
	}
	return current_pid == target_pid;
}

SEC("kprobe/page_add_new_anon_rmap")
int BPF_KPROBE(trace_page_alloc, struct page *page,
		   struct vm_area_struct *vma, unsigned long address, bool compound)
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

	/* Sampling to reduce overhead */
	if (bpf_get_prandom_u32() % 100 >= sampling_probability)
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

	event->base.pid = tgid;
	event->stack_map_sel = stack_map_sel;
	bpf_get_current_comm(&event->base.comm, sizeof(event->base.comm));

	event->base.userstack =
		bpf_get_stackid(ctx, stack_map, USER_STACKID_FLAGS);
	event->base.kernstack =
		bpf_get_stackid(ctx, stack_map, KERN_STACKID_FLAGS);

	if (event->base.userstack < 0 && event->base.kernstack < 0)
		return 0;

	event->value = 1;

	struct stack_info_t stack_info = {};
	stack_info.base.pid = event->base.pid;
	__builtin_memcpy(&stack_info.base.comm, &event->base.comm,
			 sizeof(stack_info.base.comm));
	stack_info.base.userstack = event->base.userstack;
	stack_info.base.kernstack = event->base.kernstack;
	stack_info.stack_map_sel = event->stack_map_sel;

	u64 page_addr = (u64)page;
	bpf_map_update_elem(&page_to_stackid, &page_addr, &stack_info, BPF_ANY);

	__sync_fetch_and_add(sample_count_ptr, 1);

	bpf_perf_event_output(ctx, profiler_output, COMPAT_BPF_F_CURRENT_CPU,
				  event, sizeof(*event));

	return 0;
}

SEC("kprobe/page_remove_rmap")
int BPF_KPROBE(trace_page_free, struct page *page, bool compound)
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

	u64 page_addr = (u64)page;
	struct stack_info_t *stack_info =
		bpf_map_lookup_elem(&page_to_stackid, &page_addr);
	if (!stack_info)
		return 0;

	struct mem_event_t *event = NULL;
	void *profiler_output = NULL;
	u64 *sample_count_ptr = NULL;
	u32 idx = 0;

	event = bpf_map_lookup_elem(&event_buf, &idx);
	if (!event)
		return 0;

	if (((*transfer_count_ptr) & 0x1ULL) == 0) {
		profiler_output = (void *)&profiler_output_a;
		sample_count_ptr = sample_count_ptrs[0];
	} else {
		profiler_output = (void *)&profiler_output_b;
		sample_count_ptr = sample_count_ptrs[1];
	}

	if (!event)
		return 0;

	__builtin_memset(event, 0, sizeof(*event));

	event->base.pid = stack_info->base.pid;
	event->base.kernstack = stack_info->base.kernstack;
	event->base.userstack = stack_info->base.userstack;
	event->stack_map_sel = stack_info->stack_map_sel;
	__builtin_memcpy(&event->base.comm, &stack_info->base.comm,
			 sizeof(event->base.comm));

	/* Free: negative page delta */
	event->value = -1;

	bpf_map_delete_elem(&page_to_stackid, &page_addr);

	__sync_fetch_and_add(sample_count_ptr, 1);

	bpf_perf_event_output(ctx, profiler_output, COMPAT_BPF_F_CURRENT_CPU,
				  event, sizeof(*event));

	return 0;
}

char LICENSE[] SEC("license") = "GPL";

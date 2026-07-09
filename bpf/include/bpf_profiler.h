#ifndef __BPF_PROFILER_H__
#define __BPF_PROFILER_H__

#include "bpf_common.h"
#include "bpf_cgroup.h"

/* Stack trace map default entries */
#define STACK_MAP_ENTRIES 65536

/* Stack ID flags for bpf_get_stackid */
#define KERN_STACKID_FLAGS (0)
#define USER_STACKID_FLAGS (0 | COMPAT_BPF_F_USER_STACK)

/* Profiler event base structure */
struct profiler_event_base_t {
	u64 pid_tgid;  /* tgid in upper 32 bits, pid in lower 32 bits */
	char comm[COMPAT_TASK_COMM_LEN];
	int kernstack;
	int userstack;
	s64 value;  /* CPU: sample count (1), Memory: page/byte delta */
};

/* State management indices */
typedef enum {
	PROFILER_STATE_TRANSFER_CNT_IDX = 0,
	PROFILER_STATE_SAMPLE_CNT_A_IDX,
	PROFILER_STATE_SAMPLE_CNT_B_IDX,
	PROFILER_STATE_CNT
} profiler_state_idx;

/*
 * Filter configuration injected via RewriteConstants at load time.
 * Naming follows skb_filter convention for consistency.
 */
static volatile const u64 profiler_filter_css = 0;
static volatile const u32 profiler_filter_pid = 0;
static volatile const bool profiler_filter_threads = false;
static volatile const u8 profiler_sampling_prob = 100;
static volatile const u64 profiler_idle_class_addr = 0;

/*
 * profiler_should_trace - check if current process should be traced.
 * Returns true if should trace, false otherwise.
 */
static __always_inline bool profiler_should_trace(u64 pid_tgid)
{
	u32 tgid = pid_tgid >> 32;
	u32 pid = pid_tgid & 0xffffffffUL;

	if (profiler_filter_css != 0) {
		u64 css = current_task_memory_css_addr();
		if (css != profiler_filter_css)
			return false;
	}

	if (profiler_filter_pid != 0) {
		if (profiler_filter_threads) {
			if (pid != profiler_filter_pid)
				return false;
		} else {
			if (tgid != profiler_filter_pid)
				return false;
		}
	}

	return true;
}

/*
 * profiler_should_trace_cpu - CPU profiler specific trace check.
 * Also checks idle class and cpu cgroup subsystem.
 */
static __always_inline bool profiler_should_trace_cpu(u64 pid_tgid, u64 cpu_css, u64 sched_class)
{
	u32 tgid = pid_tgid >> 32;
	u32 pid = pid_tgid & 0xffffffffUL;

	if (profiler_filter_css != 0 && profiler_filter_css != cpu_css)
		return false;

	if (profiler_filter_pid != 0 && profiler_filter_pid != tgid)
		return false;

	if (profiler_idle_class_addr != 0 && sched_class == profiler_idle_class_addr)
		return false;

	if (tgid == 0 && pid == 0)
		return false;

	return true;
}

/*
 * profiler_should_sample - probabilistic sampling.
 * Returns true if should sample, false if skip.
 */
static __always_inline bool profiler_should_sample(void)
{
	if (profiler_sampling_prob >= 100)
		return true;
	return (bpf_get_prandom_u32() % 100) < profiler_sampling_prob;
}

/*
 * profiler_fill_event_base - fill base event information.
 * Returns 0 on success, -1 if both stacks failed.
 */
static __always_inline int profiler_fill_event_base(
	struct profiler_event_base_t *event,
	void *ctx,
	void *stack_map)
{
	event->pid_tgid = bpf_get_current_pid_tgid();
	bpf_get_current_comm(&event->comm, sizeof(event->comm));

	event->userstack = bpf_get_stackid(ctx, stack_map, USER_STACKID_FLAGS);
	event->kernstack = bpf_get_stackid(ctx, stack_map, KERN_STACKID_FLAGS);

	if (event->userstack < 0 && event->kernstack < 0)
		return -1;

	return 0;
}

/*
 * DEFINE_PROFILER_MAPS - define all standard profiler maps.
 * Usage: DEFINE_PROFILER_MAPS(struct profiler_event_base_t);
 */
#define DEFINE_PROFILER_MAPS(event_type) \
struct { \
	__uint(type, BPF_MAP_TYPE_ARRAY); \
	__type(key, u32); \
	__type(value, u64); \
	__uint(max_entries, PROFILER_STATE_CNT); \
} profiler_state_map SEC(".maps"); \
\
struct { \
	__uint(type, BPF_MAP_TYPE_STACK_TRACE); \
	__uint(key_size, sizeof(u32)); \
	__uint(value_size, PERF_MAX_STACK_DEPTH * sizeof(u64)); \
	__uint(max_entries, STACK_MAP_ENTRIES); \
} stack_map_a SEC(".maps"); \
\
struct { \
	__uint(type, BPF_MAP_TYPE_STACK_TRACE); \
	__uint(key_size, sizeof(u32)); \
	__uint(value_size, PERF_MAX_STACK_DEPTH * sizeof(u64)); \
	__uint(max_entries, STACK_MAP_ENTRIES); \
} stack_map_b SEC(".maps"); \
\
struct { \
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY); \
	__uint(key_size, sizeof(int)); \
	__uint(value_size, sizeof(u32)); \
} profiler_output_a SEC(".maps"); \
\
struct { \
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY); \
	__uint(key_size, sizeof(int)); \
	__uint(value_size, sizeof(u32)); \
} profiler_output_b SEC(".maps"); \
\
struct { \
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY); \
	__uint(key_size, sizeof(u32)); \
	__uint(value_size, sizeof(event_type)); \
	__uint(max_entries, 1); \
} event_buf SEC(".maps")

/*
 * DEFINE_PROFILER_PAGE_TRACKING_MAP - define page tracking map.
 * Used by physical_alloc profiler.
 */
#define DEFINE_PROFILER_PAGE_TRACKING_MAP() \
struct { \
	__uint(type, BPF_MAP_TYPE_HASH); \
	__uint(max_entries, 1 << 20); \
	__type(key, u64); \
	__type(value, struct profiler_event_base_t); \
} page_to_stackid SEC(".maps")

/*
 * SELECT_PROFILER_AB - select A/B buffer based on transfer count parity.
 */
#define SELECT_PROFILER_AB(transfer_count_ptr, sample_count_ptrs, \
                           out_sample_count, out_stack_map, out_output) \
do { \
	if (((*(transfer_count_ptr)) & 0x1ULL) == 0) { \
		out_sample_count = sample_count_ptrs[0]; \
		out_stack_map = (void *)&stack_map_a; \
		out_output = (void *)&profiler_output_a; \
	} else { \
		out_sample_count = sample_count_ptrs[1]; \
		out_stack_map = (void *)&stack_map_b; \
		out_output = (void *)&profiler_output_b; \
	} \
} while(0)

/*
 * GET_PROFILER_STATE_POINTERS - get standard state pointers.
 */
#define GET_PROFILER_STATE_POINTERS(transfer_ptr, sample_ptrs) \
do { \
	u32 _idx = PROFILER_STATE_TRANSFER_CNT_IDX; \
	transfer_ptr = bpf_map_lookup_elem(&profiler_state_map, &_idx); \
	_idx = PROFILER_STATE_SAMPLE_CNT_A_IDX; \
	sample_ptrs[0] = bpf_map_lookup_elem(&profiler_state_map, &_idx); \
	_idx = PROFILER_STATE_SAMPLE_CNT_B_IDX; \
	sample_ptrs[1] = bpf_map_lookup_elem(&profiler_state_map, &_idx); \
} while(0)

#endif /* __BPF_PROFILER_H__ */

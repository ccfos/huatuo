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
 * profiler_should_trace - check if current process and cgroup should be traced.
 * Returns true if should trace, false otherwise.
 */
static __always_inline bool profiler_should_trace(u64 pid_tgid, u64 css)
{
	u32 tgid = pid_tgid >> 32;
	u32 pid = pid_tgid & 0xffffffffUL;

	if (profiler_filter_css != 0 && css != profiler_filter_css)
		return false;

	if (profiler_filter_pid == 0)
		return true;

	if (profiler_filter_threads)
		return tgid == profiler_filter_pid;

	return pid == profiler_filter_pid;
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
	u64 pid_tgid,
	void *ctx,
	void *stack_map)
{
	event->pid_tgid = pid_tgid;
	bpf_get_current_comm(&event->comm, sizeof(event->comm));

	event->userstack = bpf_get_stackid(ctx, stack_map, USER_STACKID_FLAGS);
	event->kernstack = bpf_get_stackid(ctx, stack_map, KERN_STACKID_FLAGS);

	if (event->userstack < 0 && event->kernstack < 0)
		return -1;

	return 0;
}

static __always_inline void profiler_copy_event_base(
	struct profiler_event_base_t *dst,
	const struct profiler_event_base_t *src)
{
	dst->pid_tgid = src->pid_tgid;
	dst->kernstack = src->kernstack;
	dst->userstack = src->userstack;
	__builtin_memcpy(&dst->comm, &src->comm, sizeof(dst->comm));
}

static __always_inline struct profiler_event_base_t *profiler_prepare_event_base(
	void *event_map,
	u64 pid_tgid,
	void *ctx,
	void *stack_map)
{
	u32 idx = 0;
	struct profiler_event_base_t *event = bpf_map_lookup_elem(event_map, &idx);
	if (!event)
		return NULL;

	__builtin_memset(event, 0, sizeof(*event));

	if (profiler_fill_event_base(event, pid_tgid, ctx, stack_map) < 0)
		return NULL;

	return event;
}

static __always_inline void profiler_emit_event(
	void *ctx,
	void *output,
	u64 *sample_count_ptr,
	void *event,
	u64 event_size)
{
	/*
	 * Global ARRAY + atomic add is intentional; do NOT switch to PERCPU.
	 * See comment in original code for details.
	 */
	__sync_fetch_and_add(sample_count_ptr, 1);
	bpf_perf_event_output(ctx, output, COMPAT_BPF_F_CURRENT_CPU,
	                      event, event_size);
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
#define SELECT_PROFILER_AB() \
do { \
	if (((*(transfer_count_ptr)) & 0x1ULL) == 0) { \
		select_profiler_sample_count_ptr = sample_count_ptrs[0]; \
		select_profiler_stack_map = (void *)&stack_map_a; \
		select_profiler_output = (void *)&profiler_output_a; \
	} else { \
		select_profiler_sample_count_ptr = sample_count_ptrs[1]; \
		select_profiler_stack_map = (void *)&stack_map_b; \
		select_profiler_output = (void *)&profiler_output_b; \
	} \
} while(0)

/*
 * profiler_init_state - initialize profiler state pointers.
 */
static __always_inline bool profiler_init_state(void *state_map, u64 **transfer_ptr, u64 **sample_ptrs)
{
	u32 idx = PROFILER_STATE_TRANSFER_CNT_IDX;

	*transfer_ptr = bpf_map_lookup_elem(state_map, &idx);
	idx = PROFILER_STATE_SAMPLE_CNT_A_IDX;
	sample_ptrs[0] = bpf_map_lookup_elem(state_map, &idx);
	idx = PROFILER_STATE_SAMPLE_CNT_B_IDX;
	sample_ptrs[1] = bpf_map_lookup_elem(state_map, &idx);

	return *transfer_ptr && sample_ptrs[0] && sample_ptrs[1];
}

#endif /* __BPF_PROFILER_H__ */

#ifndef __BPF_RATELIMIT_H__
#define __BPF_RATELIMIT_H__

#include <bpf/bpf_helpers.h>

#include "abi/bpf_ratelimit_types.h"

#define BPF_NSEC_PER_SEC 1000000000ULL

#define BPF_RATELIMIT(name, _interval_ns, _burst)                              \
	struct bpf_ratelimit_event name = {                                     \
		.interval_ns = (_interval_ns),                                    \
		.burst = (_burst),                                                \
	}

struct bpf_percpu_ratelimit_state {
	u64 window_start_ns;
	u32 events_in_window;
};

/* State must reside in a per-CPU map because updates are lockless. */
static __always_inline bool
bpf_percpu_ratelimited_ns(struct bpf_percpu_ratelimit_state *state,
			  u64 interval_ns, u32 burst)
{
	u64 now_ns;

	if (!state || !interval_ns)
		return false;

	now_ns = bpf_ktime_get_ns();
	if (!state->window_start_ns ||
	    now_ns - state->window_start_ns >= interval_ns) {
		state->window_start_ns = now_ns;
		state->events_in_window = 0;
	}

	if (state->events_in_window >= burst)
		return true;

	state->events_in_window++;
	return false;
}

// bpf_ratelimited: whether the threshold is exceeded
//
// @rate: struct bpf_ratelimit *
// @return:
//   true: the threshold is exceeded
//   false: the threshold is not exceeded
static __always_inline bool bpf_ratelimited(struct bpf_ratelimit_event *rate)
{
	u64 now_ns;
	u64 elapsed_ns;

	if (!rate || !rate->interval_ns)
		return false;

	now_ns = bpf_ktime_get_ns();

	if (!rate->window_start_ns)
		rate->window_start_ns = now_ns;

	elapsed_ns = now_ns - rate->window_start_ns;
	if (elapsed_ns >= rate->interval_ns) {
		__sync_fetch_and_add(&rate->total_elapsed_ns, elapsed_ns);
		rate->window_start_ns = now_ns;
		rate->events_in_window = 0;
		rate->missed_in_window = 0;
	}

	if (rate->events_in_window < rate->burst) {
		__sync_fetch_and_add(&rate->events_in_window, 1);
		__sync_fetch_and_add(&rate->total_events, 1);
		return false;
	}

	__sync_fetch_and_add(&rate->missed_in_window, 1);
	__sync_fetch_and_add(&rate->total_missed, 1);
	return true;
}

#define BPF_RATELIMIT_IN_MAP(name, _interval_ns, _burst, _max_burst)           \
	struct {                                                               \
		__uint(type, BPF_MAP_TYPE_ARRAY);                              \
		__uint(key_size, sizeof(u32));                                 \
		__uint(value_size, sizeof(struct bpf_ratelimit_event));        \
		__uint(max_entries, 1);                                        \
	} bpf_rlimit_##name SEC(".maps");                                      \
	struct {                                                               \
		__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);                   \
		__uint(key_size, sizeof(int));                                 \
		__uint(value_size, sizeof(u32));                               \
	} event_bpf_rlimit_##name SEC(".maps");                                \
	volatile const struct bpf_ratelimit_event ___bpf_rlimit_cfg_##name = { \
		.interval_ns = (_interval_ns),                                    \
		.burst = (_burst),                                                \
		.max_burst = (_max_burst),                                       \
	}

// bpf_ratelimited_in_map: whether the threshold is exceeded
//
// @rate: struct bpf_ratelimit *
// @return:
//   true: the threshold is exceeded
//   false: the threshold is not exceeded
#define bpf_ratelimited_in_map(ctx, rate)                                      \
	bpf_ratelimited_core_in_map(ctx, &bpf_rlimit_##rate,                   \
				    &event_bpf_rlimit_##rate,                  \
				    &___bpf_rlimit_cfg_##rate)

// BPF_RATELIMIT_IN_MAP_RC: like BPF_RATELIMIT_IN_MAP, but parameters come from
// three .rodata globals that userspace patches via cilium/ebpf RewriteConstants
// before program load instead of being baked in at compile time. Use when the
// rate must come from a CLI flag or config file. Layout matches the compile-
// time variant exactly (same state map, same perf event channel, same payload),
// so the userspace reader is interchangeable.
#define BPF_RATELIMIT_IN_MAP_RC(name)                                          \
	struct {                                                               \
		__uint(type, BPF_MAP_TYPE_ARRAY);                              \
		__uint(key_size, sizeof(u32));                                 \
		__uint(value_size, sizeof(struct bpf_ratelimit_event));        \
		__uint(max_entries, 1);                                        \
	} bpf_rlimit_##name SEC(".maps");                                      \
	struct {                                                               \
		__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);                   \
		__uint(key_size, sizeof(int));                                 \
		__uint(value_size, sizeof(u32));                               \
	} event_bpf_rlimit_##name SEC(".maps");                                \
	volatile const __u64 bpf_rlimit_interval_ns_##name = 0;                 \
	volatile const __u64 bpf_rlimit_burst_##name	 = 0;                  \
	volatile const __u64 bpf_rlimit_max_burst_##name = 0

// bpf_ratelimited_in_map_rc: same contract as bpf_ratelimited_in_map. Returns
// false (admit) when the limiter is disabled (interval_ns == 0), in a single
// .rodata load + compare with no map lookup on the fast path.
#define bpf_ratelimited_in_map_rc(ctx, name)                                   \
	({                                                                     \
		bool __ret = false;                                            \
		if (bpf_rlimit_interval_ns_##name != 0) {                      \
			struct bpf_ratelimit_event __cfg = {                   \
				.interval_ns = bpf_rlimit_interval_ns_##name,  \
				.burst	   = bpf_rlimit_burst_##name,          \
				.max_burst = bpf_rlimit_max_burst_##name,      \
			};                                                     \
			__ret = bpf_ratelimited_core_in_map(                   \
				ctx, &bpf_rlimit_##name,                       \
				&event_bpf_rlimit_##name, &__cfg);             \
		}                                                              \
		__ret;                                                         \
	})

static __always_inline bool
bpf_ratelimited_core_in_map(void *ctx, void *map, void *perf_map,
			    const volatile struct bpf_ratelimit_event *cfg)
{
	struct bpf_ratelimit_event *rate;
	u64 old_missed;
	u32 key = 0;

	rate = bpf_map_lookup_elem(map, &key);
	if (!rate)
		return false;

	if (rate->interval_ns == 0) {
		rate->interval_ns = cfg->interval_ns;
		rate->burst = cfg->burst;
		rate->max_burst = cfg->max_burst;
	}

	old_missed = rate->missed_in_window;
	if (!bpf_ratelimited(rate))
		return false;

	/* Report the first miss and configured overflow. */
	if (old_missed == 0 || (rate->max_burst > 0 &&
				 rate->missed_in_window >
				     rate->max_burst - rate->burst))
		bpf_perf_event_output(ctx, perf_map, COMPAT_BPF_F_CURRENT_CPU, rate,
				      sizeof(struct bpf_ratelimit_event));
	return true;
}

#endif

#ifndef __BPF_RATELIMIT_H__
#define __BPF_RATELIMIT_H__

#include <bpf/bpf_helpers.h>

struct bpf_ratelimit {
	uint64_t interval; // unit: second
	uint64_t begin;
	uint64_t burst;	    // max events/interval
	uint64_t max_burst; // max burst
	uint64_t events;    // current events/interval
	uint64_t nmissed;   // missed events/interval

	uint64_t total_events;	 // total events
	uint64_t total_nmissed;	 // total missed events
	uint64_t total_interval; // total interval
};

#define BPF_RATELIMIT(name, interval, burst)                                   \
	struct bpf_ratelimit name = {interval, 0, burst, 0, 0, 0, 0, 0, 0}

// bpf_ratelimited: whether the threshold is exceeded
//
// @rate: struct bpf_ratelimit *
// @return:
//   true: the threshold is exceeded
//   false: the threshold is not exceeded
static __always_inline bool bpf_ratelimited(struct bpf_ratelimit *rate)
{
	// validate
	if (rate == NULL || rate->interval == 0)
		return false;

	u64 curr = bpf_ktime_get_ns() / 1000000000;

	if (rate->begin == 0)
		rate->begin = curr;

	if (curr > rate->begin + rate->interval) {
		__sync_fetch_and_add(&rate->total_interval, curr - rate->begin);
		rate->begin  = curr;
		rate->events = rate->nmissed = 0;
	}

	if (rate->burst && rate->burst > rate->events) {
		__sync_fetch_and_add(&rate->events, 1);
		__sync_fetch_and_add(&rate->total_events, 1);
		return false;
	}

	__sync_fetch_and_add(&rate->nmissed, 1);
	__sync_fetch_and_add(&rate->total_nmissed, 1);
	return true;
}

#define BPF_RATELIMIT_IN_MAP(name, interval, burst, max_burst)                 \
	struct {                                                               \
		__uint(type, BPF_MAP_TYPE_ARRAY);                              \
		__uint(key_size, sizeof(u32));                                 \
		__uint(value_size, sizeof(struct bpf_ratelimit));              \
		__uint(max_entries, 1);                                        \
	} bpf_rlimit_##name SEC(".maps");                                      \
	struct {                                                               \
		__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);                   \
		__uint(key_size, sizeof(int));                                 \
		__uint(value_size, sizeof(u32));                               \
	} event_bpf_rlimit_##name SEC(".maps");                                \
	volatile const struct bpf_ratelimit ___bpf_rlimit_cfg_##name = {       \
		interval, 0, burst, max_burst, 0, 0, 0, 0, 0}

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
		__uint(value_size, sizeof(struct bpf_ratelimit));              \
		__uint(max_entries, 1);                                        \
	} bpf_rlimit_##name SEC(".maps");                                      \
	struct {                                                               \
		__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);                   \
		__uint(key_size, sizeof(int));                                 \
		__uint(value_size, sizeof(u32));                               \
	} event_bpf_rlimit_##name SEC(".maps");                                \
	volatile const __u64 bpf_rlimit_interval_##name	 = 0;                  \
	volatile const __u64 bpf_rlimit_burst_##name	 = 0;                  \
	volatile const __u64 bpf_rlimit_max_burst_##name = 0

// bpf_ratelimited_in_map_rc: same contract as bpf_ratelimited_in_map. Returns
// false (admit) when the limiter is disabled (interval == 0), in a single
// .rodata load + compare with no map lookup on the fast path.
#define bpf_ratelimited_in_map_rc(ctx, name)                                   \
	({                                                                     \
		bool __ret = false;                                            \
		if (bpf_rlimit_interval_##name != 0) {                         \
			struct bpf_ratelimit __cfg = {                         \
				.interval  = bpf_rlimit_interval_##name,       \
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
			    const volatile struct bpf_ratelimit *cfg)
{
	u32 key			   = 0;
	struct bpf_ratelimit *rate = NULL;

	rate = bpf_map_lookup_elem(map, &key);
	if (rate == NULL)
		return false;

	// init from cfg
	if (rate->interval == 0) {
		rate->interval	= cfg->interval;
		rate->burst	= cfg->burst;
		rate->max_burst = cfg->max_burst;
	}

	// the threshold is not exceeded, return false
	u64 old_nmissed = rate->nmissed;
	if (!bpf_ratelimited(rate))
		return false;

	// the threshold/max_burst is exceeded, notify once in a cycle
	if (old_nmissed == 0 || (rate->max_burst > 0 &&
				 rate->nmissed > rate->max_burst - rate->burst))
		bpf_perf_event_output(ctx, perf_map, COMPAT_BPF_F_CURRENT_CPU, rate,
				      sizeof(struct bpf_ratelimit));
	return true;
}

#endif

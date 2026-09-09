// SPDX-License-Identifier: Dual MIT/GPL
//
// External scheduler attribution using a BPF-side waiting bitmap.

#define BPF_NO_PRESERVE_ACCESS_INDEX
#include "vmlinux.h"
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>
#include "abi/sched_blame_types.h"
#include "bpf_common.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define MAX_CPUS 512
#define CSS_ID_BITS 12
#define MAX_CSS_IDS (1U << CSS_ID_BITS)
#define BASE_BITMAP_BITS 20
#define BASE_BITMAP_MASK ((1U << BASE_BITMAP_BITS) - 1)
#define MAX_TARGETS \
	(BASE_BITMAP_BITS + 64 * EXTRA_BITMAP_U64_COUNT)
#define NON_TARGET 0xff
#define MAX_DURATION_NS 0xffffffffULL
#define IDENTITY_EVENT_MAGIC 0x1111U
#define THROTTLE_EVENT_MAGIC 0x2222U
#define SLICE_BATCH_MAGIC 0x3333U
#define MAX_SLICES_PER_BATCH SCHED_BLAME_MAX_SLICES_PER_BATCH
#define SLICE_BATCH_MAX_AGE_NS 10000000ULL
#define TASK_RUNNING 0

volatile const u32 __SLICE_KEEP_THRESHOLD__ = 429496729U;
volatile const u32 __KEEP_ALL_SLICES__ = 0;
volatile const u32 __SLICE_BATCH_SIZE__ = MAX_SLICES_PER_BATCH;
volatile u32 target_css_id_epoch SEC(".data") = 0;

struct waiting_target_bitmap {
	// Bits 0..19 represent dense target IDs 0..19 and are copied
	// into packed_slice.packed_base bits 12..31.
	// Bits 20..31 are unused.
	u32 bitmap_base;
#if EXTRA_BITMAP_U64_COUNT > 0
	// Copied directly into packed_slice.bitmap_extra.
	u64 bitmap_extra[EXTRA_BITMAP_U64_COUNT];
#endif
};

_Static_assert(sizeof(struct sched_blame_identity_event) == 16,
	"sched-blame identity ABI changed");
_Static_assert(sizeof(struct sched_blame_throttle_event) == 8,
	"sched-blame throttle ABI changed");
_Static_assert(sizeof(struct sched_blame_packed_slice) ==
	8 * (1 + EXTRA_BITMAP_U64_COUNT),
	"sched-blame packed slice ABI changed");
_Static_assert(sizeof(struct sched_blame_slice_batch_event) ==
	8 + MAX_SLICES_PER_BATCH * sizeof(struct sched_blame_packed_slice),
	"sched-blame slice batch ABI changed");
_Static_assert(EXTRA_BITMAP_U64_COUNT <= 3,
	"sched-blame dense IDs must fit in u8");

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(u32));
	__uint(max_entries, MAX_CPUS);
} events SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, u8);
	__uint(max_entries, MAX_TARGETS);
} target_cgid_to_dense SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__type(key, u32);
	__type(value, struct sched_blame_slice_batch_event);
	__uint(max_entries, 1);
} slice_batches_percpu SEC(".maps");

#define COUNTER_MAP(name) \
	struct { \
		__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY); \
		__type(key, u32); \
		__type(value, u64); \
		__uint(max_entries, 1); \
	} name SEC(".maps")

COUNTER_MAP(n_perf_submission_failures);
COUNTER_MAP(n_perf_submission_failed_slices);
COUNTER_MAP(n_throttle_event_submission_failures);
COUNTER_MAP(n_slice_probability_drops);
COUNTER_MAP(n_idle_drops);
COUNTER_MAP(n_sampled_slices);
COUNTER_MAP(n_cpu_overflow);
COUNTER_MAP(n_css_overflow);
COUNTER_MAP(n_duration_overflow);
COUNTER_MAP(n_throttle_duration_overflow);
COUNTER_MAP(n_invalid_throttle_duration);
COUNTER_MAP(n_invalid_runnable_nr);
COUNTER_MAP(n_invalid_target_dense_id);

// Bits 32..63 hold the publication epoch; bits 0..7 hold the dense ID.
// Aligned 64-bit loads and stores keep the pair from different publications
// from being observed together.
static u64 css_id_to_dense_id[MAX_CSS_IDS];
// sched_switch writes its local value. Wakeup and migration repair may write
// remote values while the corresponding runqueue lock serializes updates.
static struct waiting_target_bitmap waiting_target_bitmap_by_cpu[MAX_CPUS];
static u32 waiting_epoch_by_cpu[MAX_CPUS];
static u64 observed_identity[MAX_CSS_IDS];
static u64 previous_switch_ns_by_cpu[MAX_CPUS];
static u64 batch_first_ts_ns_by_cpu[MAX_CPUS];
// Access only through the helpers below; zero encodes idle or unknown.
static u32 running_css_id_by_cpu[MAX_CPUS];
// The entry probe retains the argument by current hook CPU for its
// corresponding return probe.
static u64 unthrottle_entry_cfs_rq_by_cpu[MAX_CPUS];

struct task_struct___514 {
	unsigned int __state;
} __attribute__((preserve_access_index));

struct thread_info___cpu {
	u32 cpu;
} __attribute__((preserve_access_index));

struct task_struct___thread_cpu {
	struct thread_info___cpu thread_info;
} __attribute__((preserve_access_index));

struct task_struct___cpu {
	u32 cpu;
} __attribute__((preserve_access_index));

static __always_inline void count(void *map)
{
	u32 zero = 0;
	u64 *value = bpf_map_lookup_elem(map, &zero);

	if (value)
		(*value)++;
}

static __always_inline void count_add(void *map, u64 delta)
{
	u32 zero = 0;
	u64 *value = bpf_map_lookup_elem(map, &zero);

	if (value)
		*value += delta;
}

static __always_inline void pack_slice(
	u64 duration_ns,
	struct waiting_target_bitmap *waiting_snapshot,
	u64 css_index,
	struct sched_blame_packed_slice *packed)
{
	if (duration_ns > MAX_DURATION_NS) {
		count(&n_duration_overflow);
		duration_ns = MAX_DURATION_NS;
	}
	packed->packed_base = (duration_ns << 32) |
		((u64)(waiting_snapshot->bitmap_base & BASE_BITMAP_MASK) <<
		 CSS_ID_BITS) |
		css_index;
#if EXTRA_BITMAP_U64_COUNT > 0
	packed->bitmap_extra[0] = waiting_snapshot->bitmap_extra[0];
#endif
#if EXTRA_BITMAP_U64_COUNT > 1
	packed->bitmap_extra[1] = waiting_snapshot->bitmap_extra[1];
#endif
#if EXTRA_BITMAP_U64_COUNT > 2
	packed->bitmap_extra[2] = waiting_snapshot->bitmap_extra[2];
#endif
}

static __always_inline void snapshot_waiting_targets(
	u64 cpu_index, struct waiting_target_bitmap *snapshot)
{
	asm volatile("%0 &= 511" : "+r"(cpu_index));
	snapshot->bitmap_base =
		waiting_target_bitmap_by_cpu[cpu_index].bitmap_base;
#if EXTRA_BITMAP_U64_COUNT > 0
	snapshot->bitmap_extra[0] =
		waiting_target_bitmap_by_cpu[cpu_index].bitmap_extra[0];
#endif
#if EXTRA_BITMAP_U64_COUNT > 1
	snapshot->bitmap_extra[1] =
		waiting_target_bitmap_by_cpu[cpu_index].bitmap_extra[1];
#endif
#if EXTRA_BITMAP_U64_COUNT > 2
	snapshot->bitmap_extra[2] =
		waiting_target_bitmap_by_cpu[cpu_index].bitmap_extra[2];
#endif
}

static __always_inline struct task_group *task_group_of(
	struct task_struct *task)
{
	return BPF_CORE_READ(task, sched_task_group);
}

static __always_inline u32 task_group_css_id(struct task_group *tg)
{
	return (u32)BPF_CORE_READ(tg, css.id);
}

static __always_inline u32 task_pid(struct task_struct *task)
{
	return BPF_CORE_READ(task, pid);
}

static __always_inline long task_state(struct task_struct *task)
{
	struct task_struct___514 *task_514 = (void *)task;

	if (bpf_core_field_exists(task->state))
		return BPF_CORE_READ(task, state);
	return (long)BPF_CORE_READ(task_514, __state);
}

static __always_inline int task_is_runnable(struct task_struct *task)
{
	// The design assumes an exclusively CFS workload. Scheduler-class checks
	// would add work to every wakeup and migration hook.
	return task_state(task) == TASK_RUNNING;
}

static __always_inline u32 task_cpu(struct task_struct *task)
{
	struct task_struct___cpu *task_old = (void *)task;
	struct task_struct___thread_cpu *task_thread = (void *)task;

	if (bpf_core_field_exists(task_old->cpu))
		return BPF_CORE_READ(task_old, cpu);
	if (bpf_core_field_exists(task_thread->thread_info.cpu))
		return BPF_CORE_READ(task_thread, thread_info.cpu);
	return bpf_get_smp_processor_id();
}

static __always_inline u32 task_cfs_h_nr_running(struct task_struct *task)
{
	struct cfs_rq *cfs_rq = BPF_CORE_READ(task, se.cfs_rq);

	if (!cfs_rq)
		return 0;
	return BPF_CORE_READ(cfs_rq, h_nr_running);
}

static __always_inline int task_cfs_rq_is_throttled(
	struct task_struct *task)
{
	struct cfs_rq *cfs_rq = BPF_CORE_READ(task, se.cfs_rq);

	if (!cfs_rq)
		return 0;
	return BPF_CORE_READ(cfs_rq, throttled) != 0;
}

static __always_inline u64 pack_dense_id_cache(u32 epoch, u8 dense_id)
{
	return (u64)epoch << 32 | dense_id;
}

static __always_inline u32 cache_epoch(u64 cached)
{
	return (u32)(cached >> 32);
}

static __always_inline u8 cache_dense_id(u64 cached)
{
	return (u8)(cached & 0xff);
}

static __always_inline void ensure_waiting_bitmap_epoch(
	u64 cpu_index, u32 epoch)
{
	asm volatile("%0 &= 511" : "+r"(cpu_index));
	if (waiting_epoch_by_cpu[cpu_index] == epoch)
		return;

	waiting_target_bitmap_by_cpu[cpu_index].bitmap_base = 0;
#if EXTRA_BITMAP_U64_COUNT > 0
	waiting_target_bitmap_by_cpu[cpu_index].bitmap_extra[0] = 0;
#endif
#if EXTRA_BITMAP_U64_COUNT > 1
	waiting_target_bitmap_by_cpu[cpu_index].bitmap_extra[1] = 0;
#endif
#if EXTRA_BITMAP_U64_COUNT > 2
	waiting_target_bitmap_by_cpu[cpu_index].bitmap_extra[2] = 0;
#endif
	waiting_epoch_by_cpu[cpu_index] = epoch;
}

static __always_inline u32 resolve_target_dense_id(
	u64 cgid, u64 css_index, u32 epoch)
{
	volatile u64 *cache_entry;
	u64 cached;
	u8 dense_id;
	u8 *mapped;

	asm volatile("%0 &= 4095" : "+r"(css_index));
	cache_entry = &css_id_to_dense_id[css_index];
	// Preserve the verifier-bounded map-value pointer across helper calls.
	asm volatile("" : "+r"(cache_entry));
	cached = *cache_entry;
	if (cache_epoch(cached) == epoch)
		return cache_dense_id(cached);

	mapped = bpf_map_lookup_elem(&target_cgid_to_dense, &cgid);
	if (!mapped) {
		dense_id = NON_TARGET;
	} else if (*mapped >= MAX_TARGETS) {
		count(&n_invalid_target_dense_id);
		dense_id = NON_TARGET;
	} else {
		dense_id = *mapped;
	}

	*cache_entry = pack_dense_id_cache(epoch, dense_id);
	return dense_id;
}

static __always_inline void set_waiting(
	u64 cpu_index, u32 dense_id, int waiting, int cfs_rq_is_throttled)
{
	u32 base_mask;
#if EXTRA_BITMAP_U64_COUNT > 0
	u64 extra_mask;
#endif

	if (dense_id >= MAX_TARGETS) {
		count(&n_invalid_target_dense_id);
		return;
	}

	// Old verifiers can lose caller-established scalar bounds across helpers.
	asm volatile("%0 &= 511" : "+r"(cpu_index));
	if (cfs_rq_is_throttled)
		waiting = 0;

	if (dense_id < BASE_BITMAP_BITS) {
		base_mask = 1U << dense_id;
		if (waiting)
			waiting_target_bitmap_by_cpu[cpu_index].bitmap_base |=
				base_mask;
		else
			waiting_target_bitmap_by_cpu[cpu_index].bitmap_base &=
				~base_mask;
	} else {
#if EXTRA_BITMAP_U64_COUNT > 0
		dense_id -= BASE_BITMAP_BITS;
		if (dense_id < 64) {
			extra_mask = 1ULL << dense_id;
			if (waiting)
				waiting_target_bitmap_by_cpu[cpu_index].bitmap_extra[0] |=
					extra_mask;
			else
				waiting_target_bitmap_by_cpu[cpu_index].bitmap_extra[0] &=
					~extra_mask;
			return;
		}
#endif
#if EXTRA_BITMAP_U64_COUNT > 1
		dense_id -= 64;
		if (dense_id < 64) {
			extra_mask = 1ULL << dense_id;
			if (waiting)
				waiting_target_bitmap_by_cpu[cpu_index].bitmap_extra[1] |=
					extra_mask;
			else
				waiting_target_bitmap_by_cpu[cpu_index].bitmap_extra[1] &=
					~extra_mask;
			return;
		}
#endif
#if EXTRA_BITMAP_U64_COUNT > 2
		dense_id -= 64;
		extra_mask = 1ULL << dense_id;
		if (waiting)
			waiting_target_bitmap_by_cpu[cpu_index].bitmap_extra[2] |=
				extra_mask;
		else
			waiting_target_bitmap_by_cpu[cpu_index].bitmap_extra[2] &=
				~extra_mask;
#endif
	}
}

static __always_inline void set_running_css_id(
	u64 cpu_index, u64 css_index)
{
	// Add one internally so zero remains the idle or unknown sentinel.
	asm volatile("%0 &= 511" : "+r"(cpu_index));
	asm volatile("%0 &= 4095" : "+r"(css_index));
	running_css_id_by_cpu[cpu_index] = (u32)css_index + 1;
}

static __always_inline void clear_running_css_id(u64 cpu_index)
{
	asm volatile("%0 &= 511" : "+r"(cpu_index));
	running_css_id_by_cpu[cpu_index] = 0;
}

static __always_inline int cpu_is_running_css_id(
	u64 cpu_index, u64 css_index)
{
	// Hide the sentinel encoding from scheduler-state calculations.
	asm volatile("%0 &= 511" : "+r"(cpu_index));
	asm volatile("%0 &= 4095" : "+r"(css_index));
	return running_css_id_by_cpu[cpu_index] == (u32)css_index + 1;
}

static __always_inline void maybe_emit_identity(
	void *ctx, u64 event_cpu_index, struct task_group *tg, u64 css_index)
{
	struct sched_blame_identity_event event = {};
	u64 cgid;

	// event_cpu_index selects the perf entry for the CPU executing this hook.
	// It may differ from a remotely woken task's destination CPU.
	if (!tg)
		return;
	cgid = (u64)tg;
	asm volatile("%0 &= 4095" : "+r"(css_index));
	if (observed_identity[css_index] == cgid)
		return;

	event.magic = IDENTITY_EVENT_MAGIC;
	event.css_id = (u16)css_index;
	event.cgid = cgid;
	// Perf output must use the current hook CPU's perf-array entry.
	asm volatile("%0 &= 511" : "+r"(event_cpu_index));
	if (bpf_perf_event_output(ctx, &events, event_cpu_index,
				  &event, sizeof(event)) < 0) {
		return;
	}
	asm volatile("%0 &= 4095" : "+r"(css_index));
	observed_identity[css_index] = cgid;
}

static __always_inline void emit_throttle_event(
	void *ctx, u64 event_cpu_index, u32 dense_id, u64 duration_ns)
{
	struct sched_blame_throttle_event event = {};

	if (duration_ns > MAX_DURATION_NS) {
		count(&n_throttle_duration_overflow);
		duration_ns = MAX_DURATION_NS;
	}

	event.magic = THROTTLE_EVENT_MAGIC;
	event.dense_id = (u16)dense_id;
	event.duration_ns = (u32)duration_ns;
	asm volatile("%0 &= 511" : "+r"(event_cpu_index));
	if (bpf_perf_event_output(ctx, &events, event_cpu_index,
				  &event, sizeof(event)) < 0) {
		count(&n_throttle_event_submission_failures);
	}
}

#define SLICE_BATCH_OUTPUT_SIZE(capacity) \
	(sizeof(u16) * 2 + sizeof(u32) + \
	 sizeof(struct sched_blame_packed_slice) * (capacity))

static __always_inline long output_slice_batch(
	void *ctx, u64 cpu_index, struct sched_blame_slice_batch_event *batch,
	u32 count)
{
	if (count <= 1)
		return bpf_perf_event_output(ctx, &events, cpu_index, batch,
			SLICE_BATCH_OUTPUT_SIZE(1));
	if (count <= 2)
		return bpf_perf_event_output(ctx, &events, cpu_index, batch,
			SLICE_BATCH_OUTPUT_SIZE(2));
	if (count <= 4)
		return bpf_perf_event_output(ctx, &events, cpu_index, batch,
			SLICE_BATCH_OUTPUT_SIZE(4));
	if (count <= 8)
		return bpf_perf_event_output(ctx, &events, cpu_index, batch,
			SLICE_BATCH_OUTPUT_SIZE(8));
	if (count <= 16)
		return bpf_perf_event_output(ctx, &events, cpu_index, batch,
			SLICE_BATCH_OUTPUT_SIZE(16));
	if (count <= 32)
		return bpf_perf_event_output(ctx, &events, cpu_index, batch,
			SLICE_BATCH_OUTPUT_SIZE(32));
	if (count <= 64)
		return bpf_perf_event_output(ctx, &events, cpu_index, batch,
			SLICE_BATCH_OUTPUT_SIZE(64));
	return bpf_perf_event_output(ctx, &events, cpu_index, batch,
		SLICE_BATCH_OUTPUT_SIZE(128));
}

static __always_inline void emit_slice_batch(
	void *ctx, u64 cpu_index, struct sched_blame_slice_batch_event *batch)
{
	u32 submitted = batch->count;
	long result;

	if (submitted == 0 || submitted > MAX_SLICES_PER_BATCH)
		return;

	asm volatile("%0 &= 511" : "+r"(cpu_index));
	result = output_slice_batch(ctx, cpu_index, batch, submitted);
	batch->count = 0;
	asm volatile("%0 &= 511" : "+r"(cpu_index));
	batch_first_ts_ns_by_cpu[cpu_index] = 0;
	if (result < 0) {
		count(&n_perf_submission_failures);
		count_add(&n_perf_submission_failed_slices, submitted);
	}
}

static __always_inline void flush_slice_batch_if_aged(
	void *ctx, u64 cpu_index, u64 now)
{
	struct sched_blame_slice_batch_event *batch;
	u64 first;
	u32 zero = 0;

	asm volatile("%0 &= 511" : "+r"(cpu_index));
	first = batch_first_ts_ns_by_cpu[cpu_index];
	if (first == 0 || now - first < SLICE_BATCH_MAX_AGE_NS)
		return;

	batch = bpf_map_lookup_elem(&slice_batches_percpu, &zero);
	if (batch && batch->count != 0) {
		emit_slice_batch(ctx, cpu_index, batch);
		return;
	}

	asm volatile("%0 &= 511" : "+r"(cpu_index));
	batch_first_ts_ns_by_cpu[cpu_index] = 0;
}

static __always_inline void append_sampled_slice(
	void *ctx,
	u64 cpu_index,
	u64 now,
	struct sched_blame_packed_slice *packed_slice)
{
	struct sched_blame_slice_batch_event *batch;
	u32 slice_batch_size = __SLICE_BATCH_SIZE__;
	u32 index;
	u32 zero = 0;

	if (slice_batch_size < 1)
		slice_batch_size = 1;
	if (slice_batch_size > MAX_SLICES_PER_BATCH)
		slice_batch_size = MAX_SLICES_PER_BATCH;

	batch = bpf_map_lookup_elem(&slice_batches_percpu, &zero);
	if (!batch)
		return;

	index = batch->count;
	if (index >= MAX_SLICES_PER_BATCH) {
		emit_slice_batch(ctx, cpu_index, batch);
		index = 0;
	}
	asm volatile("%0 &= 127" : "+r"(index));

	if (index == 0) {
		batch->magic = SLICE_BATCH_MAGIC;
		batch->extra_bitmap_u64_count = EXTRA_BITMAP_U64_COUNT;
		asm volatile("%0 &= 511" : "+r"(cpu_index));
		batch_first_ts_ns_by_cpu[cpu_index] = now;
	}

	batch->packed_slices[index].packed_base = packed_slice->packed_base;
#if EXTRA_BITMAP_U64_COUNT > 0
	batch->packed_slices[index].bitmap_extra[0] =
		packed_slice->bitmap_extra[0];
#endif
#if EXTRA_BITMAP_U64_COUNT > 1
	batch->packed_slices[index].bitmap_extra[1] =
		packed_slice->bitmap_extra[1];
#endif
#if EXTRA_BITMAP_U64_COUNT > 2
	batch->packed_slices[index].bitmap_extra[2] =
		packed_slice->bitmap_extra[2];
#endif
	batch->count = index + 1;
	if (batch->count >= slice_batch_size)
		emit_slice_batch(ctx, cpu_index, batch);
}

static __always_inline int keep_slice(void)
{
	if (__KEEP_ALL_SLICES__)
		return 1;
	return bpf_get_prandom_u32() < __SLICE_KEEP_THRESHOLD__;
}

// TP_PROTO(bool preempt, struct task_struct *prev, struct task_struct *next)
SEC("raw_tracepoint/sched_switch")
int on_sched_switch(struct bpf_raw_tracepoint_args *ctx)
{
	struct task_struct *prev = (void *)ctx->args[1];
	struct task_struct *next = (void *)ctx->args[2];
	struct task_group *prev_tg;
	struct task_group *next_tg;
	u32 cpu = bpf_get_smp_processor_id();
	u64 now = bpf_ktime_get_ns();
	u64 cpu_index;
	u64 prev_start_ns;
	u64 prev_css_index = 0;
	u64 next_css_index = 0;
	struct waiting_target_bitmap waiting_snapshot = {};
	struct sched_blame_packed_slice packed_slice = {};
	u32 prev_pid;
	u32 next_pid;
	u32 prev_css_id = 0;
	u32 next_css_id = 0;
	u32 prev_dense_id;
	u32 next_dense_id;
	u32 epoch;

	if (cpu >= MAX_CPUS) {
		count(&n_cpu_overflow);
		return 0;
	}
	// Use 64-bit masked indexes so older verifiers retain a non-negative bound.
	cpu_index = cpu;
	asm volatile("%0 &= 511" : "+r"(cpu_index));
	epoch = target_css_id_epoch;
	ensure_waiting_bitmap_epoch(cpu_index, epoch);
	prev_start_ns = previous_switch_ns_by_cpu[cpu_index];
	previous_switch_ns_by_cpu[cpu_index] = now;
	flush_slice_batch_if_aged(ctx, cpu_index, now);

	prev_pid = task_pid(prev);
	next_pid = task_pid(next);
	prev_tg = prev_pid == 0 ? NULL : task_group_of(prev);
	next_tg = next_pid == 0 ? NULL : task_group_of(next);

	if (prev_tg) {
		prev_css_id = task_group_css_id(prev_tg);
		if (prev_css_id >= MAX_CSS_IDS) {
			count(&n_css_overflow);
		} else {
			prev_css_index = prev_css_id;
			asm volatile("%0 &= 4095" : "+r"(prev_css_index));
			maybe_emit_identity(ctx, cpu_index, prev_tg,
				prev_css_index);
		}
	}
	if (next_tg) {
		next_css_id = task_group_css_id(next_tg);
		if (next_css_id >= MAX_CSS_IDS) {
			count(&n_css_overflow);
		} else {
			next_css_index = next_css_id;
			asm volatile("%0 &= 4095" : "+r"(next_css_index));
			maybe_emit_identity(ctx, cpu_index, next_tg,
				next_css_index);
		}
	}

	// The pre-switch bitmap describes the interval in which prev ran.
	if (prev_start_ns != 0) {
		if (prev_pid == 0) {
			count(&n_idle_drops);
		} else if (prev_tg && prev_css_id < MAX_CSS_IDS) {
			if (!keep_slice()) {
				count(&n_slice_probability_drops);
			} else {
				snapshot_waiting_targets(
					cpu_index, &waiting_snapshot);
				pack_slice(
					now - prev_start_ns,
					&waiting_snapshot,
					prev_css_index,
					&packed_slice);
				count(&n_sampled_slices);
				append_sampled_slice(ctx, cpu_index, now,
					&packed_slice);
			}
		}
	}

	// Establish waiting truth for the interval in which next will run.
	// This always executes, regardless of whether the slice was sampled.
	if (prev_tg && prev_css_id < MAX_CSS_IDS) {
		prev_dense_id = resolve_target_dense_id(
			(u64)prev_tg, prev_css_index, epoch);
		if (prev_dense_id != NON_TARGET)
			set_waiting(
				cpu_index,
				prev_dense_id,
				task_cfs_h_nr_running(prev) >= 1,
				task_cfs_rq_is_throttled(prev));
	}
	clear_running_css_id(cpu_index);
	if (next_tg && next_css_id < MAX_CSS_IDS) {
		// Apply next second so a same-cgroup switch gets its final state.
		next_dense_id = resolve_target_dense_id(
			(u64)next_tg, next_css_index, epoch);
		if (next_dense_id != NON_TARGET)
			set_waiting(
				cpu_index,
				next_dense_id,
				task_cfs_h_nr_running(next) >= 2,
				task_cfs_rq_is_throttled(next));
		set_running_css_id(cpu_index, next_css_index);
	}
	return 0;
}

static __always_inline void update_wakeup_waiting(
	struct bpf_raw_tracepoint_args *ctx, struct task_struct *task)
{
	struct task_group *tg;
	u32 event_cpu = bpf_get_smp_processor_id();
	u32 dst_cpu = task_cpu(task);
	u32 css_id;
	u32 runnable_nr;
	u32 waiting_nr;
	u32 dense_id;
	u32 epoch;
	u64 event_cpu_index;
	u64 dst_cpu_index;
	u64 css_index;

	if (event_cpu >= MAX_CPUS || dst_cpu >= MAX_CPUS) {
		count(&n_cpu_overflow);
		return;
	}
	if (!task_is_runnable(task))
		return;

	// A self-wakeup changes task state without enqueueing new work.
	if (task == (struct task_struct *)bpf_get_current_task())
		return;

	tg = task_group_of(task);
	if (!tg)
		return;
	css_id = task_group_css_id(tg);
	if (css_id >= MAX_CSS_IDS) {
		count(&n_css_overflow);
		return;
	}

	event_cpu_index = event_cpu;
	dst_cpu_index = dst_cpu;
	css_index = css_id;
	asm volatile("%0 &= 511" : "+r"(event_cpu_index));
	asm volatile("%0 &= 511" : "+r"(dst_cpu_index));
	asm volatile("%0 &= 4095" : "+r"(css_index));
	epoch = target_css_id_epoch;
	ensure_waiting_bitmap_epoch(dst_cpu_index, epoch);

	// Identity output is local to the hook CPU. Waiting state belongs to the
	// destination CPU selected for the woken task.
	maybe_emit_identity(ctx, event_cpu_index, tg, css_index);

	dense_id = resolve_target_dense_id((u64)tg, css_index, epoch);
	if (dense_id == NON_TARGET)
		return;

	runnable_nr = task_cfs_h_nr_running(task);
	if (runnable_nr == 0) {
		count(&n_invalid_runnable_nr);
		return;
	}
	if (cpu_is_running_css_id(dst_cpu_index, css_index)) {
		// Exclude the same-cgroup task running on the destination CPU.
		waiting_nr = runnable_nr - 1;
	} else {
		// Another cgroup owns the CPU, so every runnable entity waits.
		waiting_nr = runnable_nr;
	}
	set_waiting(
		dst_cpu_index,
		dense_id,
		waiting_nr >= 1,
		task_cfs_rq_is_throttled(task));
}

// TP_PROTO(struct task_struct *p)
SEC("raw_tracepoint/sched_wakeup")
int on_sched_wakeup(struct bpf_raw_tracepoint_args *ctx)
{
	struct task_struct *task = (void *)ctx->args[0];

	update_wakeup_waiting(ctx, task);
	return 0;
}

// TP_PROTO(struct task_struct *p)
SEC("raw_tracepoint/sched_wakeup_new")
int on_sched_wakeup_new(struct bpf_raw_tracepoint_args *ctx)
{
	struct task_struct *task = (void *)ctx->args[0];

	update_wakeup_waiting(ctx, task);
	return 0;
}

// TP_PROTO(struct task_struct *p, int dest_cpu)
SEC("raw_tracepoint/sched_migrate_task")
int on_sched_migrate_task(struct bpf_raw_tracepoint_args *ctx)
{
	struct task_struct *task = (void *)ctx->args[0];
	u32 dst_cpu = (u32)ctx->args[1];
	struct task_group *tg;
	u32 src_cpu;
	u32 css_id;
	u32 src_cpu_runnable_nr;
	u32 src_cpu_waiting_nr;
	u32 dense_id;
	u32 epoch;
	u64 src_cpu_index;
	u64 css_index;

	// Wakeup placement does not remove runnable work from a source cfs_rq.
	if (!task_is_runnable(task))
		return 0;

	// task_cpu() still identifies the source at this tracepoint.
	src_cpu = task_cpu(task);
	if (src_cpu >= MAX_CPUS || dst_cpu >= MAX_CPUS) {
		count(&n_cpu_overflow);
		return 0;
	}

	tg = task_group_of(task);
	if (!tg)
		return 0;
	css_id = task_group_css_id(tg);
	if (css_id >= MAX_CSS_IDS) {
		count(&n_css_overflow);
		return 0;
	}

	src_cpu_index = src_cpu;
	css_index = css_id;
	asm volatile("%0 &= 511" : "+r"(src_cpu_index));
	asm volatile("%0 &= 4095" : "+r"(css_index));
	epoch = target_css_id_epoch;
	ensure_waiting_bitmap_epoch(src_cpu_index, epoch);
	dense_id = resolve_target_dense_id((u64)tg, css_index, epoch);
	if (dense_id == NON_TARGET)
		return 0;

	// Normal CFS migration has removed task from the source cfs_rq while
	// retaining the source runqueue lock. Account for a same-cgroup task that
	// is currently running when interpreting the remaining runnable count.
	src_cpu_runnable_nr = task_cfs_h_nr_running(task);
	if (src_cpu_runnable_nr == 0) {
		set_waiting(
			src_cpu_index,
			dense_id,
			0,
			task_cfs_rq_is_throttled(task));
		return 0;
	}

	if (cpu_is_running_css_id(src_cpu_index, css_index)) {
		// Exclude the same-cgroup task currently running on the source CPU.
		src_cpu_waiting_nr = src_cpu_runnable_nr - 1;
	} else {
		// Another cgroup owns the CPU, so all remaining tasks are waiting.
		src_cpu_waiting_nr = src_cpu_runnable_nr;
	}
	set_waiting(
		src_cpu_index,
		dense_id,
		src_cpu_waiting_nr >= 1,
		task_cfs_rq_is_throttled(task));

	// Do not update the destination bitmap remotely. Its next relevant wakeup
	// or sched_switch establishes the state; temporary destination
	// under-reporting is an accepted approximation.
	return 0;
}

SEC("kprobe/unthrottle_cfs_rq")
int on_unthrottle_cfs_rq_entry(struct pt_regs *ctx)
{
	struct cfs_rq *cfs_rq = (void *)PT_REGS_PARM1(ctx);
	u32 event_cpu = bpf_get_smp_processor_id();
	u64 event_cpu_index;

	if (event_cpu >= MAX_CPUS) {
		count(&n_cpu_overflow);
		return 0;
	}
	event_cpu_index = event_cpu;
	asm volatile("%0 &= 511" : "+r"(event_cpu_index));
	unthrottle_entry_cfs_rq_by_cpu[event_cpu_index] = (u64)cfs_rq;
	return 0;
}

SEC("kretprobe/unthrottle_cfs_rq")
int on_unthrottle_cfs_rq_return(struct pt_regs *ctx)
{
	struct cfs_rq *cfs_rq;
	struct task_group *tg;
	struct rq *rq;
	u32 event_cpu = bpf_get_smp_processor_id();
	u32 target_cpu;
	u32 css_id;
	u32 dense_id;
	u32 epoch;
	u64 event_cpu_index;
	u64 target_cpu_index;
	u64 css_index;
	u64 end_clock;
	u64 start_clock;

	if (event_cpu >= MAX_CPUS) {
		count(&n_cpu_overflow);
		return 0;
	}
	event_cpu_index = event_cpu;
	asm volatile("%0 &= 511" : "+r"(event_cpu_index));
	cfs_rq = (void *)unthrottle_entry_cfs_rq_by_cpu[event_cpu_index];
	unthrottle_entry_cfs_rq_by_cpu[event_cpu_index] = 0;
	if (!cfs_rq || BPF_CORE_READ(cfs_rq, throttled))
		return 0;

	rq = BPF_CORE_READ(cfs_rq, rq);
	tg = BPF_CORE_READ(cfs_rq, tg);
	if (!rq || !tg)
		return 0;
	target_cpu = BPF_CORE_READ(rq, cpu);
	if (target_cpu >= MAX_CPUS) {
		count(&n_cpu_overflow);
		return 0;
	}
	css_id = task_group_css_id(tg);
	if (css_id >= MAX_CSS_IDS) {
		count(&n_css_overflow);
		return 0;
	}

	target_cpu_index = target_cpu;
	css_index = css_id;
	asm volatile("%0 &= 511" : "+r"(target_cpu_index));
	asm volatile("%0 &= 4095" : "+r"(css_index));
	epoch = target_css_id_epoch;
	ensure_waiting_bitmap_epoch(target_cpu_index, epoch);
	dense_id = resolve_target_dense_id((u64)tg, css_index, epoch);
	if (dense_id == NON_TARGET)
		return 0;

	// The group entity has just been re-enqueued on target_cpu. None of this
	// cfs_rq's runnable tasks can already be current there.
	set_waiting(
		target_cpu_index,
		dense_id,
		BPF_CORE_READ(cfs_rq, h_nr_running) >= 1,
		0);

	end_clock = BPF_CORE_READ(rq, clock);
	start_clock = BPF_CORE_READ(cfs_rq, throttled_clock);
	if (end_clock < start_clock) {
		count(&n_invalid_throttle_duration);
		return 0;
	}
	emit_throttle_event(
		ctx,
		event_cpu_index,
		dense_id,
		end_clock - start_clock);
	return 0;
}

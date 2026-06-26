#include "vmlinux.h"

#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define HW_ERR_MCE	    0
#define HW_ERR_EDAC	    1
#define HW_ERR_NON_STANDARD 2
#define HW_ERR_AER_EVENT    3
#define HW_ERR_THR	    4
#define HW_ERR_ARM_GHES	    5

#define MCI_STATUS_DEFERRED (1ULL << 44)
#define MCI_STATUS_UC	    (1ULL << 61)

struct event {
	u32 type;
	u32 pad0;
	u64 timestamp;
	u8 info[512];
};

/* Per-CPU scratch buffer used to build events without consuming stack space. */
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(struct event));
	__uint(max_entries, 1);
} event_data_map SEC(".maps");

/* Perf-event array for delivering RAS hardware error events to userspace. */
struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} ras_event_map SEC(".maps");

/*
 * event_init - initialise a per-CPU event slot.
 *
 * Zero the entire struct so that fields not explicitly set by a given probe
 * (e.g. corrected for AER / non-standard events) do not carry stale values
 * from a previous invocation on the same CPU.
 */
static __always_inline void event_init(struct event *event, u32 type)
{
	__builtin_memset(event, 0, sizeof(*event));
	event->timestamp = bpf_ktime_get_ns();
	event->type	 = type;
}

/*
 * event_size - compute the byte size of a tracepoint's dynamic payload.
 *
 * A Linux __data_loc field encodes the absolute byte offset of the dynamic
 * data in the low 16 bits and its length in the high 16 bits; the total
 * span (offset + length) gives the number of bytes that must be read from
 * the tracepoint context, clamped to 512.
 */
static __always_inline u32 event_size(u32 data_loc)
{
	u32 size = (data_loc & 0xffff) + ((data_loc >> 16) & 0xffff);
	return size > 512 ? 512 : size;
}

#ifdef __TARGET_ARCH_x86

SEC("tracepoint/mce/mce_record")
int probe_mce_record(struct trace_event_raw_mce_record *ctx)
{
	struct event *event;
	int key = 0;

	event = bpf_map_lookup_elem(&event_data_map, &key);
	if (!event)
		return 0;

	event_init(event, HW_ERR_MCE);

	bpf_probe_read(event->info, sizeof(struct trace_event_raw_mce_record),
		       ctx);

	bpf_perf_event_output(ctx, &ras_event_map, COMPAT_BPF_F_CURRENT_CPU,
			      event, sizeof(struct event));
	return 0;
}

/* thr_info is stored in event->info for HW_ERR_THR events. */
struct thr_info {
	u32 vector;
	u32 cpu;
};

/*
 * thr_apic_ctx mirrors DECLARE_EVENT_CLASS(vector_apic): struct trace_entry
 * (8 bytes) + int vector.  Defined locally because vmlinux.h does not include
 * the shared trace_event_raw_vector_apic class struct.
 */
struct thr_apic_ctx {
	struct trace_entry ent;
	int vector;
};

SEC("tracepoint/irq_vectors/threshold_apic_entry")
int probe_thr_apic(struct thr_apic_ctx *ctx)
{
	struct event *event;
	int key = 0;

	event = bpf_map_lookup_elem(&event_data_map, &key);
	if (!event)
		return 0;

	event_init(event, HW_ERR_THR);

	struct thr_info thr = {
		.vector = (u32)ctx->vector,
		.cpu	= bpf_get_smp_processor_id(),
	};
	bpf_probe_read(event->info, sizeof(thr), &thr);

	bpf_perf_event_output(ctx, &ras_event_map, COMPAT_BPF_F_CURRENT_CPU,
			      event, sizeof(struct event));
	return 0;
}

#endif /* __TARGET_ARCH_x86 */

#ifdef __TARGET_ARCH_arm64

SEC("tracepoint/ras/arm_event")
int probe_arm_event(struct trace_event_raw_arm_event *ctx)
{
	struct event *event;
	int key = 0;

	event = bpf_map_lookup_elem(&event_data_map, &key);
	if (!event)
		return 0;

	event_init(event, HW_ERR_ARM_GHES);

	/*
	 * arm_event tracepoint does not carry GHES severity; the GHES driver
	 * drops it before calling trace_arm_event(). Severity is reported as
	 * unknown in userspace.
	 */
	bpf_probe_read(event->info, sizeof(struct trace_event_raw_arm_event), ctx);

	bpf_perf_event_output(ctx, &ras_event_map, COMPAT_BPF_F_CURRENT_CPU,
			      event, sizeof(struct event));
	return 0;
}

#endif /* __TARGET_ARCH_arm64 */

SEC("tracepoint/ras/mc_event")
int probe_ras_mc_event(struct trace_event_raw_mc_event *ctx)
{
	struct event *event;
	int key = 0;

	event = bpf_map_lookup_elem(&event_data_map, &key);
	if (!event)
		return 0;

	event_init(event, HW_ERR_EDAC);

	bpf_probe_read(event->info, event_size(ctx->__data_loc_driver_detail),
		       ctx);

	bpf_perf_event_output(ctx, &ras_event_map, COMPAT_BPF_F_CURRENT_CPU,
			      event, sizeof(struct event));
	return 0;
}

SEC("tracepoint/ras/non_standard_event")
int probe_ras_non_standard(struct trace_event_raw_non_standard_event *ctx)
{
	int key = 0;
	struct event *event;

	event = bpf_map_lookup_elem(&event_data_map, &key);
	if (!event)
		return 0;

	event_init(event, HW_ERR_NON_STANDARD);

	/*
	 * Severity is embedded in the payload; correctedness is determined
	 * from it in userspace, so corrected stays 0 (set by event_init).
	 */
	bpf_probe_read(event->info, event_size(ctx->__data_loc_buf), ctx);

	bpf_perf_event_output(ctx, &ras_event_map, COMPAT_BPF_F_CURRENT_CPU,
			      event, sizeof(struct event));
	return 0;
}

SEC("tracepoint/ras/aer_event")
int probe_ras_aer_event(struct trace_event_raw_aer_event *ctx)
{
	int key = 0;
	struct event *event;

	event = bpf_map_lookup_elem(&event_data_map, &key);
	if (!event)
		return 0;

	event_init(event, HW_ERR_AER_EVENT);

	/*
	 * AER severity is embedded in the payload; error type is resolved from
	 * it in userspace, so corrected stays 0 (set by event_init).
	 */
	bpf_probe_read(event->info, event_size(ctx->__data_loc_dev_name), ctx);

	bpf_perf_event_output(ctx, &ras_event_map, COMPAT_BPF_F_CURRENT_CPU,
			      event, sizeof(struct event));
	return 0;
}

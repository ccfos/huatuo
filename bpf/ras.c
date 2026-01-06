#include "vmlinux.h"

#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define ERR_MCE 0
#define ERR_EDAC 1
#define ERR_APIC_NON_STANDARD 2
#define ERR_AER 3

#define MCI_STATUS_DEFERRED (1ULL << 44)
#define MCI_STATUS_UC (1ULL << 61)

struct report_event {
	u32 type;
	u32 corrected;
	u64 timestamp;
	u8 info[512];
};

// the map use for storing struct report_event memory
struct {
	__uint(type, BPF_MAP_TYPE_PERCPU_ARRAY);
	__uint(key_size, sizeof(u32)); // key = 0
	__uint(value_size, sizeof(struct report_event));
	__uint(max_entries, 1);
} report_map SEC(".maps");

// the event map use for report userspace
struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(u32));
} ras_event_map SEC(".maps");

void event_init(struct report_event *event, u32 type)
{
	event->timestamp = bpf_ktime_get_ns();
	event->type	 = type;
	__builtin_memset(event->info, 0, sizeof(event->info));
}

/*
 * The over all size of the trace output equals to the last
 * __data_loc_* and the lengh of the string.
 */
u32 get_event_size(u32 last_data_loc)
{
	u32 size = (last_data_loc & 0xffff) +
		    ((last_data_loc >> 16) & 0xffff);
	size = size > 512 ? 512 : size;

	return size;
}

#ifdef __TARGET_ARCH_x86
SEC("tracepoint/mce/mce_record")
void probe_mce_record(struct trace_event_raw_mce_record *ctx)
{
	int key = 0;
	struct report_event *event;

	event = bpf_map_lookup_elem(&report_map, &key);
	if (!event)
		return;

	event_init(event, ERR_MCE);
	bpf_probe_read(event->info, sizeof(struct trace_event_raw_mce_record),
		       ctx);

	// uncorrected error and deferred (AMD specific)
	if ((ctx->cpuvendor == 2 && ctx->status & MCI_STATUS_DEFERRED) ||
	    (ctx->status & MCI_STATUS_UC))
		event->corrected = 0;
	else
		event->corrected = 1;

	bpf_perf_event_output(ctx, &ras_event_map, COMPAT_BPF_F_CURRENT_CPU,
			      event, sizeof(struct report_event));
}
#endif

SEC("tracepoint/ras/mc_event")
void probe_ras_mc_event(struct trace_event_raw_mc_event *ctx)
{
	struct report_event *event;
	int key = 0;
	u32 size;

	event = bpf_map_lookup_elem(&report_map, &key);
	if (!event)
		return;

	event_init(event, ERR_EDAC);

	event->corrected = ctx->error_type ? 0 : 1;

	size = get_event_size(ctx->__data_loc_driver_detail);

	bpf_probe_read(event->info, size, ctx);

	bpf_perf_event_output(ctx, &ras_event_map, COMPAT_BPF_F_CURRENT_CPU,
			      event, sizeof(struct report_event));
}

SEC("tracepoint/ras/non_standard_event")
void probe_ras_non_standard(struct trace_event_raw_non_standard_event *ctx)
{
	struct report_event *event;
	int key = 0;
	u32 size;

	event = bpf_map_lookup_elem(&report_map, &key);
	if (!event)
		return;

	event_init(event, ERR_APIC_NON_STANDARD);

	size = get_event_size(ctx->__data_loc_buf);

	bpf_probe_read(event->info, size, ctx);

	bpf_perf_event_output(ctx, &ras_event_map, COMPAT_BPF_F_CURRENT_CPU,
			      event, sizeof(struct report_event));
}

SEC("tracepoint/ras/aer_event")
void probe_ras_aer_event(struct trace_event_raw_aer_event *ctx)
{
	struct report_event *event;
	int key = 0;
	u32 size;

	event = bpf_map_lookup_elem(&report_map, &key);
	if (!event)
		return;

	event_init(event, ERR_AER);

	size = get_event_size(ctx->__data_loc_dev_name);
	bpf_probe_read(event->info, size, ctx);

	bpf_perf_event_output(ctx, &ras_event_map, COMPAT_BPF_F_CURRENT_CPU,
			      event, sizeof(struct report_event));
}

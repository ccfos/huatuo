#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

#include "bpf_common.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define BPF_DEV_MINOR_BITS 20
#define BPF_DEV_MINOR_MASK ((1U << BPF_DEV_MINOR_BITS) - 1)

#define REQ_OP_BITS 8
#define REQ_OP_MASK ((1 << REQ_OP_BITS) - 1)

/*
 * Linux 4.18-5.15 use bit 11 for RQF_QUIET. The completion fallback is
 * attached only when block_rq_error is absent, so this compatibility value
 * is deliberately limited to those kernels.
 */
#define RQF_QUIET_BEFORE_5_16 (1U << 11)

enum health_event_type {
	HEALTH_EVENT_BLOCK_ERROR = 1,
	HEALTH_EVENT_SCSI_TIMEOUT,
	HEALTH_EVENT_SCSI_DISPATCH_ERROR,
};

struct health_event {
	u64 sector;
	u32 dev;
	s32 status;
	u32 host;
	u32 channel;
	u32 target;
	u32 lun;
	u8 type;
	u8 operation;
	u8 pad[6];
};

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(u32));
} health_events SEC(".maps");

struct request_queue___5_14 {
	struct gendisk *disk;
} __attribute__((preserve_access_index));

/* block_rq_error uses this event class from Linux 5.18 onward. */
struct trace_event_raw_block_rq_completion___io_health {
	struct trace_entry ent;
	dev_t dev;
	sector_t sector;
	unsigned int nr_sector;
	int error;
	char rwbs[2];
} __attribute__((preserve_access_index));

/*
 * SCSI trace-event types may live in module BTF. Mirror the stable prefix
 * from include/trace/events/scsi.h instead of depending on those types.
 */
struct io_health_scsi_timeout_ctx {
	u64 trace_entry;
	u32 host_no;
	u32 channel;
	u32 id;
	u32 lun;
};

struct io_health_scsi_dispatch_error_ctx {
	u64 trace_entry;
	u32 host_no;
	u32 channel;
	u32 id;
	u32 lun;
	s32 rtn;
};

static __always_inline u32 encode_dev(u32 major, u32 minor)
{
	return (major & 0xfff) << BPF_DEV_MINOR_BITS |
	       (minor & BPF_DEV_MINOR_MASK);
}

static __always_inline struct gendisk *request_disk(struct request *req)
{
	struct request_queue___5_14 *q = NULL;

	if (bpf_core_field_exists(req->rq_disk))
		return BPF_CORE_READ(req, rq_disk);

	if (bpf_core_field_exists(q->disk)) {
		q = (struct request_queue___5_14 *)BPF_CORE_READ(req, q);
		return BPF_CORE_READ(q, disk);
	}
	return NULL;
}

static __always_inline u32 request_dev(struct request *req)
{
	struct gendisk *disk;
	u32 major, minor;

	disk = request_disk(req);
	if (!disk)
		return 0;
	/*
	 * A partition dev_t may come from the extended-device IDR, so it cannot
	 * be derived from first_minor + partno. Health events are attributed to
	 * the containing disk, whose dev_t is major:first_minor.
	 */
	major = BPF_CORE_READ(disk, major);
	minor = BPF_CORE_READ(disk, first_minor);
	return encode_dev(major, minor);
}

static __always_inline u8 request_operation(struct request *req)
{
	return BPF_CORE_READ(req, cmd_flags) & REQ_OP_MASK;
}

static __always_inline u8 block_tracepoint_operation(
	struct trace_event_raw_block_rq_completion___io_health *ctx)
{
	char operation = BPF_CORE_READ(ctx, rwbs[0]);

	/* REQ_PREFLUSH precedes the request operation, for example "FW". */
	if (operation == 'F') {
		char next = BPF_CORE_READ(ctx, rwbs[1]);

		if (next == 'R' || next == 'W' || next == 'D' || next == 'F')
			operation = next;
	}

	switch (operation) {
	case 'R':
		return REQ_OP_READ;
	case 'W':
		return REQ_OP_WRITE;
	case 'F':
		return REQ_OP_FLUSH;
	case 'D':
		return REQ_OP_DISCARD;
	default:
		return REQ_OP_MASK;
	}
}

static __always_inline void submit_event(void *ctx, struct health_event *event)
{
	bpf_perf_event_output(ctx, &health_events, COMPAT_BPF_F_CURRENT_CPU,
				      event, sizeof(*event));
}

static __always_inline int submit_block_error(void *ctx, struct request *req,
					      s32 status)
{
	struct health_event event = {
		.type = HEALTH_EVENT_BLOCK_ERROR,
		.status = status,
	};

	if (!req)
		return 0;
	event.dev = request_dev(req);
	event.sector = BPF_CORE_READ(req, __sector);
	event.operation = request_operation(req);
	submit_event(ctx, &event);
	return 0;
}

SEC("tracepoint/block/block_rq_error")
int trace_block_rq_error(
	struct trace_event_raw_block_rq_completion___io_health *ctx)
{
	struct health_event event = {
		.type = HEALTH_EVENT_BLOCK_ERROR,
	};

	if (!bpf_core_type_exists(
		    struct trace_event_raw_block_rq_completion___io_health))
		return 0;

	event.dev = BPF_CORE_READ(ctx, dev);
	event.sector = BPF_CORE_READ(ctx, sector);
	event.status = BPF_CORE_READ(ctx, error);
	event.operation = block_tracepoint_operation(ctx);
	submit_event(ctx, &event);
	return 0;
}

/*
 * block_rq_error was added in Linux 5.18. Go attaches this completion
 * program only on Linux 5.15 and earlier, where args[1] is a negative errno.
 * Non-negative values are still rejected defensively.
 *
 * Match the dedicated error tracepoint's old-kernel emission rules closely
 * so passthrough health commands and quiet requests do not recursively
 * trigger another health collection.
 */
SEC("raw_tracepoint/block_rq_complete")
int trace_block_rq_complete_error(struct bpf_raw_tracepoint_args *ctx)
{
	struct request *req = (struct request *)ctx->args[0];
	s32 status = (s32)ctx->args[1];
	u32 operation;
	u32 rq_flags;

	if (status >= 0 || !req)
		return 0;

	/* blk_update_request() returns before reporting errors without a bio. */
	if (!BPF_CORE_READ(req, bio))
		return 0;

	/* Match blk_rq_is_passthrough(), which print_req_error() excludes. */
	operation = BPF_CORE_READ(req, cmd_flags) & REQ_OP_MASK;
	if (operation == REQ_OP_SCSI_IN || operation == REQ_OP_SCSI_OUT ||
	    operation == REQ_OP_DRV_IN || operation == REQ_OP_DRV_OUT)
		return 0;

	/* Match print_req_error(), which suppresses requests marked RQF_QUIET. */
	rq_flags = BPF_CORE_READ(req, rq_flags);
	if (rq_flags & RQF_QUIET_BEFORE_5_16)
		return 0;

	return submit_block_error(ctx, req, status);
}

SEC("tracepoint/scsi/scsi_dispatch_cmd_timeout")
int trace_scsi_timeout(struct io_health_scsi_timeout_ctx *ctx)
{
	struct health_event event = {
		.type = HEALTH_EVENT_SCSI_TIMEOUT,
		.host = ctx->host_no,
		.channel = ctx->channel,
		.target = ctx->id,
		.lun = ctx->lun,
	};

	submit_event(ctx, &event);
	return 0;
}

SEC("tracepoint/scsi/scsi_dispatch_cmd_error")
int trace_scsi_dispatch_error(struct io_health_scsi_dispatch_error_ctx *ctx)
{
	struct health_event event = {
		.type = HEALTH_EVENT_SCSI_DISPATCH_ERROR,
		.status = ctx->rtn,
		.host = ctx->host_no,
		.channel = ctx->channel,
		.target = ctx->id,
		.lun = ctx->lun,
	};

	submit_event(ctx, &event);
	return 0;
}

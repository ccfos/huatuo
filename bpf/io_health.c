/*
 * Copyright 2026 The HuaTuo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

/* Capture kernel IO health events and their device identity. Evidence
 * commands and persistence stay in userspace to keep probe work bounded.
 */
#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

#include "bpf_common.h"
#include "abi/io_health_types.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define BPF_DEV_MINOR_BITS 20
#define BPF_DEV_MINOR_MASK ((1U << BPF_DEV_MINOR_BITS) - 1)

#define REQ_OP_BITS 8
#define REQ_OP_MASK ((1 << REQ_OP_BITS) - 1)

/* Macro-based request flags use bit 11; Go relocates enum-based flags. */
const volatile u32 io_health_rqf_quiet_mask = 1U << 11;

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(u32));
} health_events SEC(".maps");

struct request_queue___5_14 {
	struct gendisk *disk;
} __attribute__((preserve_access_index));

/* The dedicated error tracepoint uses this event class. */
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

static __always_inline void submit_event(void *ctx, struct io_health_event *event)
{
	bpf_perf_event_output(ctx, &health_events, COMPAT_BPF_F_CURRENT_CPU,
				      event, sizeof(*event));
}

static __always_inline int submit_block_error(void *ctx, struct request *req,
					      s32 status)
{
	struct io_health_event event = {
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
	struct io_health_event event = {
		.type = HEALTH_EVENT_BLOCK_ERROR,
		.operation = REQ_OP_MASK,
	};

	if (!bpf_core_field_exists(ctx->error))
		return 0;

	event.dev = BPF_CORE_READ(ctx, dev);
	event.sector = BPF_CORE_READ(ctx, sector);
	event.status = BPF_CORE_READ(ctx, error);
	BPF_CORE_READ_INTO(&event.rwbs, ctx, rwbs);
	submit_event(ctx, &event);
	return 0;
}

/*
 * Use completion when the dedicated error tracepoint is unavailable.
 * Its stable rq/status slots carry either a negative errno or a positive
 * blk_status_t; userspace selects the interpretation by sign.
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

	if (!status || !req)
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
	if (rq_flags & io_health_rqf_quiet_mask)
		return 0;

	return submit_block_error(ctx, req, status);
}

SEC("tracepoint/scsi/scsi_dispatch_cmd_timeout")
int trace_scsi_timeout(struct io_health_scsi_timeout_ctx *ctx)
{
	struct io_health_event event = {
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
	struct io_health_event event = {
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

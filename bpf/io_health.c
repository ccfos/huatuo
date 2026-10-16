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
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "abi/io_health_types.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define BPF_DEV_MINOR_BITS 20
#define BPF_DEV_MINOR_MASK ((1U << BPF_DEV_MINOR_BITS) - 1)

#define REQ_OP_BITS 8
#define REQ_OP_MASK ((1 << REQ_OP_BITS) - 1)

#define NVME_CTRL_NAME_ENTRIES 256
#define NVME_CDEV_CALL_ENTRIES 256
#define NVME_STATE_CALL_ENTRIES 1024
#define IO_HEALTH_FREEZE_ENTRIES 128

/* Only freeze events touch this table. Disk registration resets a reused
 * device number; removal releases its row. No queue identity cache is needed.
 */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, IO_HEALTH_FREEZE_ENTRIES);
	__type(key, u64);
	__type(value, u64);
} io_health_freeze_counts SEC(".maps");

/* Buffered events must not collect evidence from a replacement device.
 * Snapshot this word before resolving an event's kernel identity; Go checks
 * it after resolving the command target. Ordinary IO never reads this map.
 */
struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__uint(max_entries, 1);
	__type(key, u32);
	__type(value, u64);
} io_health_topology SEC(".maps");

/* Macro-based request flags use bit 11; Go relocates enum-based flags. */
const volatile u32 io_health_rqf_quiet_mask = 1U << 11;

enum health_event_type {
	HEALTH_EVENT_BLOCK_ERROR = 1,
	HEALTH_EVENT_SCSI_TIMEOUT,
	HEALTH_EVENT_SCSI_DISPATCH_ERROR,
	HEALTH_EVENT_NVME_TIMEOUT,
	HEALTH_EVENT_NVME_RESET,
	HEALTH_EVENT_NVME_STATE_CHANGE,
};

struct nvme_ctrl_name {
	char name[IO_HEALTH_CONTROLLER_NAME_LEN];
};

struct nvme_state_call {
	u64 ctrl;
	u32 new_state_raw;
	u32 pad;
};

struct nvme_cdev_call {
	u64 ctrl;
	struct nvme_ctrl_name name;
};

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(u32));
} health_events SEC(".maps");

/* The key is an opaque struct nvme_ctrl pointer learned through sysfs. */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, NVME_CTRL_NAME_ENTRIES);
	__type(key, u64);
	__type(value, struct nvme_ctrl_name);
} nvme_ctrl_names SEC(".maps");

/* Pair cdev add/delete calls; delete returns after sysfs callbacks drain. */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, NVME_CDEV_CALL_ENTRIES);
	__type(key, u64);
	__type(value, struct nvme_cdev_call);
} nvme_cdev_calls SEC(".maps");

/* Pair nvme_change_ctrl_state entry and return without reading private BTF. */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, NVME_STATE_CALL_ENTRIES);
	__type(key, u64);
	__type(value, struct nvme_state_call);
} nvme_state_calls SEC(".maps");

struct request_queue___5_14 {
	struct gendisk *disk;
} __attribute__((preserve_access_index));

static __always_inline u64 io_health_topology_generation(bool changed)
{
	u32 key = 0;
	u64 *generation = bpf_map_lookup_elem(&io_health_topology, &key);

	if (!generation)
		return 0;
	if (changed)
		__sync_fetch_and_add(generation, 1);
	return *generation;
}

/* Queue registration follows sysfs publication. Advance the generation at
 * device_add entry so late old-disk events cannot resolve to the replacement
 * during that gap. Device types distinguish disks from unrelated devices.
 */
SEC("kprobe/device_add")
int io_health_disk_publishing(struct pt_regs *ctx)
{
	struct device *dev = (void *)PT_REGS_PARM1_CORE(ctx);
	const struct device_type *type;
	const char *type_name;
	char name[6] = {};

	if (!dev)
		return 0;
	type = BPF_CORE_READ(dev, type);
	if (!type)
		return 0;
	type_name = BPF_CORE_READ(type, name);
	if (bpf_probe_read_str(name, sizeof(name), type_name) != sizeof("disk"))
		return 0;
	if (name[0] == 'd' && name[1] == 'i' &&
	    name[2] == 's' && name[3] == 'k')
		io_health_topology_generation(true);
	return 0;
}

static __always_inline u64 io_health_freeze_key(struct gendisk *disk)
{
	char name[5] = {};
	u32 major, minor;

	if (!disk || bpf_core_read(name, sizeof(name), &disk->disk_name))
		return 0;
	if (!((name[0] == 's' && name[1] == 'd') ||
	      (name[0] == 'v' && name[1] == 'd') ||
	      (name[0] == 'n' && name[1] == 'v' &&
	       name[2] == 'm' && name[3] == 'e')))
		return 0;
	major = BPF_CORE_READ(disk, major);
	minor = BPF_CORE_READ(disk, first_minor);
	return ((u64)major << 32) | minor;
}

SEC("kprobe/blk_register_queue")
int io_health_freeze_disk_added(struct pt_regs *ctx)
{
	u64 key;
	u64 zero = 0;

	key = io_health_freeze_key((void *)PT_REGS_PARM1_CORE(ctx));
	if (key)
		bpf_map_update_elem(&io_health_freeze_counts, &key, &zero,
				    COMPAT_BPF_ANY);
	return 0;
}

SEC("kprobe/del_gendisk")
int io_health_freeze_disk_removed(struct pt_regs *ctx)
{
	u64 key;

	io_health_topology_generation(true);
	key = io_health_freeze_key((void *)PT_REGS_PARM1_CORE(ctx));
	if (key)
		bpf_map_delete_elem(&io_health_freeze_counts, &key);
	return 0;
}

SEC("kprobe/blk_mq_freeze_queue")
int io_health_freeze_queue(struct pt_regs *ctx)
{
	struct request_queue *q = (void *)PT_REGS_PARM1_CORE(ctx);
	struct request_queue___5_14 *current = (void *)q;
	struct gendisk *disk;
	struct kobject *parent;
	u64 key, one = 1;
	u64 *count;

	if (bpf_core_field_exists(current->disk)) {
		disk = BPF_CORE_READ(current, disk);
	} else {
		/* Registered legacy queues are children of disk_to_dev(disk).
		 * Unregistered queues have no disk association and are skipped.
		 */
		parent = BPF_CORE_READ(q, kobj.parent);
		if (!parent)
			return 0;
		disk = (void *)((char *)parent -
			compat_bpf_core_field_offset(((struct device *)0)->kobj) -
			compat_bpf_core_field_offset(((struct gendisk *)0)->part0.__dev));
	}
	key = io_health_freeze_key(disk);
	if (!key)
		return 0;
	count = bpf_map_lookup_elem(&io_health_freeze_counts, &key);
	if (!count) {
		if (!bpf_map_update_elem(&io_health_freeze_counts, &key, &one,
					 COMPAT_BPF_NOEXIST))
			return 0;
		count = bpf_map_lookup_elem(&io_health_freeze_counts, &key);
	}
	if (count)
		__sync_fetch_and_add(count, 1);
	return 0;
}

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

static __always_inline void set_nvme_controller(struct io_health_event *event,
						u64 ctrl)
{
	struct nvme_ctrl_name *name;

	name = bpf_map_lookup_elem(&nvme_ctrl_names, &ctrl);
	if (name)
		__builtin_memcpy(event->controller, name->name,
				 sizeof(event->controller));
}

static __always_inline bool read_nvme_controller(struct device *dev,
						  u64 *ctrl,
						  struct nvme_ctrl_name *name)
{
	char class_name[5] = {};
	struct class *class;
	const char *name_ptr;
	const char *class_name_ptr;

	if (!dev)
		return false;
	class = BPF_CORE_READ(dev, class);
	if (!class)
		return false;
	class_name_ptr = BPF_CORE_READ(class, name);
	if (!class_name_ptr)
		return false;
	/* Linux 4.18 exposes only the compatible kernel string helper. */
	if (bpf_probe_read_str(class_name, sizeof(class_name),
			       class_name_ptr) != sizeof(class_name))
		return false;
	if (class_name[0] != 'n' || class_name[1] != 'v' ||
	    class_name[2] != 'm' || class_name[3] != 'e' || class_name[4])
		return false;

	*ctrl = (u64)BPF_CORE_READ(dev, driver_data);
	name_ptr = BPF_CORE_READ(dev, kobj.name);
	if (!*ctrl || !name_ptr)
		return false;
	return bpf_probe_read_str(name->name, sizeof(name->name), name_ptr) > 1;
}

static __always_inline int submit_block_error(void *ctx, struct request *req,
					      s32 status)
{
	struct io_health_event event = {
		.topology_generation = io_health_topology_generation(false),
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
		.topology_generation = io_health_topology_generation(false),
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
		.topology_generation = io_health_topology_generation(false),
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
		.topology_generation = io_health_topology_generation(false),
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

/*
 * nvme_sysfs_show_state() obtains its controller with dev_get_drvdata(). Go
 * reads each existing controller state file once during startup, then detaches
 * this bootstrap hook. cdev_device_add/delete maintain subsequent hotplug
 * changes without NVMe module BTF.
 */
SEC("kprobe/nvme_sysfs_show_state")
int BPF_KPROBE(kprobe_nvme_sysfs_show_state, struct device *dev)
{
	struct nvme_ctrl_name name = {};
	u64 ctrl;

	if (!read_nvme_controller(dev, &ctrl, &name))
		return 0;
	bpf_map_update_elem(&nvme_ctrl_names, &ctrl, &name, COMPAT_BPF_ANY);
	return 0;
}

SEC("kprobe/cdev_device_add")
int BPF_KPROBE(kprobe_nvme_cdev_add, struct cdev *cdev, struct device *dev)
{
	struct nvme_cdev_call call = {};
	u64 pid_tgid = bpf_get_current_pid_tgid();

	(void)cdev;
	if (read_nvme_controller(dev, &call.ctrl, &call.name))
		bpf_map_update_elem(&nvme_cdev_calls, &pid_tgid, &call,
				    COMPAT_BPF_ANY);
	return 0;
}

SEC("kretprobe/cdev_device_add")
int BPF_KRETPROBE(kretprobe_nvme_cdev_add)
{
	struct nvme_ctrl_name name = {};
	u64 pid_tgid = bpf_get_current_pid_tgid();
	struct nvme_cdev_call *call;
	u64 ctrl;

	call = bpf_map_lookup_elem(&nvme_cdev_calls, &pid_tgid);
	if (!call)
		return 0;
	if (!PT_REGS_RC(ctx)) {
		ctrl = call->ctrl;
		__builtin_memcpy(name.name, call->name.name, sizeof(name.name));
		bpf_map_update_elem(&nvme_ctrl_names, &ctrl, &name,
				    COMPAT_BPF_ANY);
	}
	bpf_map_delete_elem(&nvme_cdev_calls, &pid_tgid);
	return 0;
}

SEC("kprobe/cdev_device_del")
int BPF_KPROBE(kprobe_nvme_cdev_del, struct cdev *cdev, struct device *dev)
{
	struct nvme_cdev_call call = {};
	u64 pid_tgid = bpf_get_current_pid_tgid();

	(void)cdev;
	if (read_nvme_controller(dev, &call.ctrl, &call.name))
		bpf_map_update_elem(&nvme_cdev_calls, &pid_tgid, &call,
				    COMPAT_BPF_ANY);
	return 0;
}

SEC("kretprobe/cdev_device_del")
int BPF_KRETPROBE(kretprobe_nvme_cdev_del)
{
	u64 pid_tgid = bpf_get_current_pid_tgid();
	struct nvme_cdev_call *call;
	u64 ctrl;

	call = bpf_map_lookup_elem(&nvme_cdev_calls, &pid_tgid);
	if (!call)
		return 0;
	ctrl = call->ctrl;
	bpf_map_delete_elem(&nvme_cdev_calls, &pid_tgid);
	/* cdev_device_del has drained sysfs callbacks, including bootstrap. */
	bpf_map_delete_elem(&nvme_ctrl_names, &ctrl);
	return 0;
}

SEC("kprobe/nvme_timeout")
int BPF_KPROBE(kprobe_nvme_timeout, struct request *req)
{
	struct io_health_event event = {
		.topology_generation = io_health_topology_generation(false),
		.type = HEALTH_EVENT_NVME_TIMEOUT,
	};

	if (!req)
		return 0;
	event.dev = request_dev(req);
	submit_event(ctx, &event);
	return 0;
}

SEC("kprobe/nvme_reset_ctrl")
int BPF_KPROBE(kprobe_nvme_reset, void *raw_ctrl)
{
	struct io_health_event event = {
		.topology_generation = io_health_topology_generation(false),
		.type = HEALTH_EVENT_NVME_RESET,
	};

	if (raw_ctrl)
		set_nvme_controller(&event, (u64)raw_ctrl);
	submit_event(ctx, &event);
	return 0;
}

SEC("kprobe/nvme_change_ctrl_state")
int BPF_KPROBE(kprobe_nvme_change_state, void *raw_ctrl, u32 new_state)
{
	struct nvme_state_call call = {
		.ctrl = (u64)raw_ctrl,
		.new_state_raw = new_state,
	};
	u64 pid_tgid = bpf_get_current_pid_tgid();

	if (call.ctrl)
		bpf_map_update_elem(&nvme_state_calls, &pid_tgid, &call,
				    COMPAT_BPF_ANY);
	return 0;
}

SEC("kretprobe/nvme_change_ctrl_state")
int BPF_KRETPROBE(kretprobe_nvme_change_state)
{
	struct io_health_event event = {
		.topology_generation = io_health_topology_generation(false),
		.type = HEALTH_EVENT_NVME_STATE_CHANGE,
	};
	u64 pid_tgid = bpf_get_current_pid_tgid();
	struct nvme_state_call *call;

	call = bpf_map_lookup_elem(&nvme_state_calls, &pid_tgid);
	if (!call)
		return 0;
	if (PT_REGS_RC(ctx)) {
		set_nvme_controller(&event, call->ctrl);
		event.new_state_raw = call->new_state_raw;
		submit_event(ctx, &event);
	}
	bpf_map_delete_elem(&nvme_state_calls, &pid_tgid);
	return 0;
}

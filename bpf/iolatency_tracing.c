/*
 * Measure read/write IO on enrolled SCSI/SAS, NVMe and virtio queues. Q/A
 * counts queued size; the bio's kernel submission timestamp supplies Q,
 * request timestamps supply G and the last D, and raw request completion
 * supplies C before the kernel advances the bio chain.
 * This boundary excludes upper DM/MD processing and bio completion callbacks.
 * Host counters gate disk selection. Device changes invalidate the session.
 * No per-bio state survives a probe: an IO ending before request completion
 * needs no cleanup. Missing kernel Q timestamps skip Q2G/Q2D only. The kernel
 * owns submission timing, including resubmission, splitting and plug caching;
 * it does not guarantee the exact Q/A tracepoint time.
 */
#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_blkio.h"
#include "bpf_common.h"
#include "bpf_iocontrol.h"
#include "bpf_compat_7_0.h"

char __license[] SEC("license") = "Dual MIT/GPL";

IO_CONTROL_DEVICE_HOOKS()

#define IO_LATENCY_BUCKETS 17
#define IO_SIZE_BUCKETS 6
#define IO_SIZE_KIB 1024ULL
#define IO_LATENCY_REQUEST_SCAN_BUDGET 512
#define IO_LATENCY_REQUEST_CHUNK_BIOS 32
#define IO_LATENCY_COMPLETION_CHUNK_COUNT \
	((IO_LATENCY_REQUEST_SCAN_BUDGET - 1 + IO_LATENCY_REQUEST_CHUNK_BIOS - 1) / \
	 IO_LATENCY_REQUEST_CHUNK_BIOS)
#define IO_LATENCY_DISKS 128
#define IO_LATENCY_CONTAINER_CSS 2048
#define IO_LATENCY_OPERATIONS 2
#define IO_LATENCY_CONTAINER_SERIES \
	(IO_LATENCY_CONTAINER_CSS * IO_LATENCY_DISKS * \
	 IO_LATENCY_OPERATIONS)
#define IO_LATENCY_CHAIN_OVERFLOW 1
#define IO_LATENCY_ENOENT 2
#define IO_LATENCY_EBUSY 16
#define IO_LATENCY_EFAULT 14
#define IO_LATENCY_EEXIST 17

#define IO_LATENCY_STAGE_Q2D 0
#define IO_LATENCY_STAGE_D2C 1
#define IO_LATENCY_STAGE_Q2G 2

#define IO_SIZE_POINT_QUEUE 0
#define IO_SIZE_POINT_ISSUE 1

#define IO_LATENCY_NS_PER_US 1000ULL
#define IO_LATENCY_NS_PER_MS 1000000ULL
#define IO_LATENCY_NS_PER_SEC 1000000000ULL

#ifndef REQ_OP_MASK
#define REQ_OP_MASK ((1U << 8) - 1)
#endif

/* Go probes each raw tracepoint's argument count before loading. */
volatile const u32 block_bio_queue_bio_arg = 0;
volatile const u32 block_bio_remap_bio_arg = 0;
volatile const u32 block_rq_remap_request_arg = 0;
volatile const u32 io_latency_bio_trace_completion = 10;
volatile const u64 io_latency_root_blkcg = 0;
volatile const bool io_latency_containers_enabled = true;
/* CSS entries cache the kernel serial until actual release. */
struct io_latency_group_state {
	u64 serial;
};

/* A completion IRQ can first observe several CSS identities before the
 * allocator can refill. This small BPF-only index has a fixed bound.
 */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct io_latency_group_state);
	__uint(max_entries, IO_LATENCY_CONTAINER_CSS);
} io_latency_group_map SEC(".maps");

struct latency_counters {
	u64 q2d[IO_LATENCY_BUCKETS];
	u64 d2c[IO_LATENCY_BUCKETS];
	u64 q2g[IO_LATENCY_BUCKETS];
	u64 queued_size[IO_SIZE_BUCKETS];
	u64 issued_size[IO_SIZE_BUCKETS];
};

static const struct latency_counters zero_latency_counters = {};

struct host_latency_counters {
	u32 major;
	u32 minor;
	struct latency_counters counters[IO_LATENCY_OPERATIONS];
	u64 scsi;
};

struct container_latency_key {
	u64 blkcg;
	u64 serial;
	u32 major;
	u32 minor;
	u32 operation;
	u32 kind;
};

/* A final CSS release publishes one row in the same statistical stream.
 * Userspace deletes that serial's counter rows, then the end record. Keeping
 * the record until cleanup succeeds needs no independent retirement queue.
 */
#define IO_LATENCY_CSS_RELEASED 1
#define IO_LATENCY_GROUP_INSERT_FAILED 3
#define IO_LATENCY_GROUP_RELEASE_FAILED 4

/*
 * Keep failure handling out of per-bio accounting to bound older verifier
 * state. Map operations return signed 32-bit errno; consume their results as
 * int so direct JIT calls and helper wrappers use the same comparison width.
 * One aligned word publishes the reason and signed helper errno. Userspace
 * checks it at collection boundaries and detaches the session on error.
 */
static __noinline void io_latency_fail(u32 reason, int error)
{
	struct io_control_status *status;

	/* A nested hash writer can return EBUSY before changing the map.
	 * Skip that write: missed samples are accepted.
	 * Retrying here cannot release the interrupted writer's guard.
	 */
	if (error == -IO_LATENCY_EBUSY)
		return;
	status = io_control_status_value();

	if (status)
		status->failure = ((u64)reason << 32) | (u32)error;
}

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct host_latency_counters);
	__uint(max_entries, IO_LATENCY_DISKS);
	__uint(map_flags, COMPAT_BPF_F_NO_PREALLOC);
} blkdisk_lat_map SEC(".maps");

/*
 * Each CSS can emit one series per disk and read/write operation. Keep the
 * full cardinality bound sparse so only observed series allocate counters.
 */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, struct container_latency_key);
	__type(value, struct latency_counters);
	__uint(max_entries, IO_LATENCY_CONTAINER_SERIES + IO_LATENCY_CONTAINER_CSS);
	__uint(map_flags, COMPAT_BPF_F_NO_PREALLOC);
} blkcg_lat_map SEC(".maps");

/* Disk registration is a cold path under a sysfs-owned disk reference.
 * Device changes invalidate the session at the next collection boundary.
 */
/* The queue sysfs kobject's parent is the whole-disk device. Its owning
 * structure changed from embedded hd_struct to block_device. Relocate that
 * ownership directly; queue_attr_show keeps the same kobject argument.
 */
struct block_device___queue_probe {
	struct device bd_device;
	struct gendisk *bd_disk;
} __attribute__((preserve_access_index));

struct blk_queue_stats___queue_probe {
	int accounting;
} __attribute__((preserve_access_index));

/* Use the builtin also provided by legacy CO-RE headers. */
#define QUEUE_PROBE_CONTAINER(ptr, type, member) \
	((type *)((char *)(ptr) - __builtin_preserve_field_info( \
		((type *)0)->member, BPF_FIELD_BYTE_OFFSET)))
static __always_inline struct gendisk *io_latency_device_disk(struct device *device)
{
	if (bpf_core_field_exists(((struct block_device___queue_probe *)0)->bd_device)) {
		struct block_device___queue_probe *bdev = QUEUE_PROBE_CONTAINER(
			device, struct block_device___queue_probe, bd_device);

		return BPF_CORE_READ(bdev, bd_disk);
	}
	return (struct gendisk *)QUEUE_PROBE_CONTAINER(device,
		struct gendisk, part0.__dev);
}

struct queue_probe_key {
	char name[32]; /* DISK_NAME_LEN includes the terminating zero. */
};

struct queue_probe_result {
	__s64 result;
	__u32 major;
	__u32 minor;
	__u64 disk;
};

/* Results use the disk name read from sysfs. BPF_EXIST keeps a late reader
 * from recreating a removed result or overwriting another disk's result.
 */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 1);
	__type(key, struct queue_probe_key);
	__type(value, struct queue_probe_result);
} io_latency_probe_result SEC(".maps");

/* Each requested read of queue/iostats also samples the state which controls
 * QUEUE_FLAG_STATS: accounting enabled or at least one stats callback.
 * This follows blk-stat.c without depending on flag bit numbers. G is the
 * iostats value returned by sysfs. Results: 1 skipped, 2 supported,
 * 3 disabled; negative values are errno.
 */
SEC("kprobe/queue_attr_show")
int probe_queue_stats(struct pt_regs *ctx)
{
	struct kobject *kobj = (struct kobject *)PT_REGS_PARM1_CORE(ctx);
	struct attribute *attr = (struct attribute *)PT_REGS_PARM2_CORE(ctx);
	struct kobject *parent;
	struct device *device;
	struct gendisk *disk;
	struct request_queue *q;
	const struct blk_mq_ops *mq_ops;
	struct blk_queue_stats *stats;
	struct list_head *next;
	struct queue_probe_result result = {};
	__u64 disk_key;
	const char *disk_name, *attr_name;
	struct queue_probe_key key = {};
	char attribute[9] = {};
	__u32 major, minor;
	int accounting = 0, ret;
	bool enabled = false;

	/* Any reader of the requested disk's iostats can finish the probe. */
	ret = BPF_CORE_READ_INTO(&attr_name, attr, name);
	if (ret)
		return 0;
	ret = bpf_probe_read_str(attribute, sizeof(attribute), attr_name);
	if (ret != 8 || attribute[0] != 'i' ||
	    attribute[1] != 'o' || attribute[2] != 's' ||
	    attribute[3] != 't' || attribute[4] != 'a' ||
	    attribute[5] != 't' || attribute[6] != 's' || attribute[7])
		return 0;
	ret = BPF_CORE_READ_INTO(&parent, kobj, parent);
	if (ret)
		return 0;
	ret = BPF_CORE_READ_INTO(&disk_name, parent, name);
	if (ret)
		return 0;
	ret = bpf_probe_read_str(key.name, sizeof(key.name), disk_name);
	if (ret < 0)
		return 0;
	/* Hidden NVMe paths have a sysfs name but no device.devt. */
	if (!bpf_map_lookup_elem(&io_latency_probe_result, &key))
		return 0;
	device = QUEUE_PROBE_CONTAINER(parent, struct device, kobj);
	disk = io_latency_device_disk(device);
	if (!disk) {
		ret = -IO_LATENCY_EFAULT;
		goto failed;
	}
	ret = BPF_CORE_READ_INTO(&q, disk, queue);
	if (ret)
		goto failed;
	/* Multipath heads forward bios; only NVMe MQ paths issue requests. */
	if (key.name[0] == 'n' && key.name[1] == 'v' &&
	    key.name[2] == 'm' && key.name[3] == 'e') {
		ret = BPF_CORE_READ_INTO(&mq_ops, q, mq_ops);
		if (ret)
			goto failed;
		if (!mq_ops) {
			result.result = 1;
			goto finished;
		}
	}
	ret = BPF_CORE_READ_INTO(&stats, q, stats);
	if (ret)
		goto failed;
	ret = BPF_CORE_READ_INTO(&next, stats, callbacks.next);
	if (ret)
		goto failed;
	if (bpf_core_field_exists(((struct blk_queue_stats___queue_probe *)0)->accounting)) {
		ret = BPF_CORE_READ_INTO(&accounting,
			(struct blk_queue_stats___queue_probe *)stats, accounting);
		enabled = accounting != 0;
	} else {
		ret = BPF_CORE_READ_INTO(&enabled, stats, enable_accounting);
	}
	if (ret)
		goto failed;
	ret = BPF_CORE_READ_INTO(&major, disk, major);
	if (ret)
		goto failed;
	ret = BPF_CORE_READ_INTO(&minor, disk, first_minor);
	if (ret)
		goto failed;

	disk_key = (__u64)disk;
	result.major = major;
	result.minor = minor;
	result.disk = disk_key;
	if (!enabled && next == &stats->callbacks) {
		result.result = 3;
		goto finished;
	}
	/* Go owns initial Host publication after accepting this queue check. */
	result.result = 2;
finished:
	/* Report the state observed by this read. Replace the whole value so
	 * no writable pointer survives result deletion or HASH node reuse.
	 */
	bpf_map_update_elem(&io_latency_probe_result, &key, &result, COMPAT_BPF_EXIST);
	return 0;
failed:
	result.result = ret;
	goto finished;
}

/* Thresholds are below 2^63. Values with the high bit set exceed all of
 * them; otherwise the subtraction's high bit gives the comparison.
 * The first bucket uses one fast return; arithmetic classification of
 * larger waits bounds verifier work across the 512-bio walk.
 */
static __always_inline u32 io_histogram_above(u64 value, u64 threshold)
{
	u64 distance = threshold - value;

	asm volatile("" : "+r"(distance));
	return (distance | value) >> 63;
}

/* Scalar inputs allow independent timestamp and bucket checks on kernels
 * with global-function verification. Keep these decisions in one helper;
 * older verifiers also need the optional-path ordering in the bio walk.
 */
__noinline int io_latency_bucket(u64 start_ns, u64 end_ns, u64 queue_stage)
{
	u64 ns;
	u32 bucket = 0;

	if (!start_ns || !end_ns)
		return -1;
	/* Kernel plug caching and request merging can put G/D before Q.
	 * Preserve those samples in the first bucket without claiming exact
	 * per-event timing. Reversed D2C remains invalid.
	 */
	if (end_ns < start_ns)
		return queue_stage ? 0 : -1;
	ns = end_ns - start_ns;
	if (ns <= 50 * IO_LATENCY_NS_PER_US)
		return 0;
	bucket = 1 + io_histogram_above(ns, 100 * IO_LATENCY_NS_PER_US) +
		io_histogram_above(ns, 250 * IO_LATENCY_NS_PER_US) +
		io_histogram_above(ns, 500 * IO_LATENCY_NS_PER_US) +
		io_histogram_above(ns, IO_LATENCY_NS_PER_MS);
	bucket += io_histogram_above(ns, 2 * IO_LATENCY_NS_PER_MS);
	bucket += io_histogram_above(ns, 4 * IO_LATENCY_NS_PER_MS);
	bucket += io_histogram_above(ns, 8 * IO_LATENCY_NS_PER_MS);
	bucket += io_histogram_above(ns, 16 * IO_LATENCY_NS_PER_MS);
	bucket += io_histogram_above(ns, 32 * IO_LATENCY_NS_PER_MS);
	bucket += io_histogram_above(ns, 64 * IO_LATENCY_NS_PER_MS);
	bucket += io_histogram_above(ns, 128 * IO_LATENCY_NS_PER_MS);
	bucket += io_histogram_above(ns, 256 * IO_LATENCY_NS_PER_MS);
	bucket += io_histogram_above(ns, 512 * IO_LATENCY_NS_PER_MS);
	bucket += io_histogram_above(ns, IO_LATENCY_NS_PER_SEC);
	bucket += io_histogram_above(ns, 2 * IO_LATENCY_NS_PER_SEC);
	return bucket;
}

static __always_inline int io_size_bucket(u32 bytes)
{
	u32 bucket = 0;

	if (bytes <= 4 * IO_SIZE_KIB)
		return 0;
	bucket += io_histogram_above(bytes, 4 * IO_SIZE_KIB);
	bucket += io_histogram_above(bytes, 16 * IO_SIZE_KIB);
	bucket += io_histogram_above(bytes, 64 * IO_SIZE_KIB);
	bucket += io_histogram_above(bytes, 256 * IO_SIZE_KIB);
	bucket += io_histogram_above(bytes, 1024 * IO_SIZE_KIB);
	return bucket;
}

/*
 * Raw tracepoint arguments moved when request_queue was removed from several
 * block tracepoints. Keep every context access at a fixed offset so kernels
 * whose verifier rejects variable raw-context offsets can load this program.
 */
static __always_inline u64 io_latency_pointer_argument(
	struct bpf_raw_tracepoint_args *ctx, u32 index)
{
	u64 argument;

	if (index == 0) {
		argument = ctx->args[0];
		asm volatile("" : "+r"(argument));
		return argument;
	}
	argument = ctx->args[1];
	asm volatile("" : "+r"(argument));
	return argument;
}

static __always_inline void increment_latency(
	struct latency_counters *counters, u32 stage, int bucket)
{
	if (stage == IO_LATENCY_STAGE_Q2D)
		__sync_fetch_and_add(&counters->q2d[bucket], 1);
	else if (stage == IO_LATENCY_STAGE_D2C)
		__sync_fetch_and_add(&counters->d2c[bucket], 1);
	else if (stage == IO_LATENCY_STAGE_Q2G)
		__sync_fetch_and_add(&counters->q2g[bucket], 1);
}

static __always_inline struct host_latency_counters *lookup_host_counters(
	struct gendisk *disk)
{
	u64 key = (u64)disk;

	/* Disk registration supplies identity and both operations in one lookup. */
	return bpf_map_lookup_elem(&blkdisk_lat_map, &key);
}

/* The current bio pins its blkg and CSS. C reads that owner rather than
 * replaying a cached address from a Q/A which may have been missed.
 */
/* Scalar calls isolate identity/cache branches from each bio-walk state. */
__noinline u64 io_latency_bio_css(u64 address)
{
	struct bio *bio = (void *)address;
	struct blkcg *blkcg;

	blkcg = BPF_CORE_READ(bio, bi_blkg, blkcg);
	if (!blkcg || (u64)blkcg == io_latency_root_blkcg)
		return 0;
	return (u64)blkcg + compat_bpf_core_field_offset(
		((struct blkcg *)0)->css);
}

__noinline u64 io_latency_css_serial(u64 css)
{
	struct io_latency_group_state initial = {};
	struct io_latency_group_state *group;
	struct cgroup_subsys_state *state = (void *)css;
	int ret;

	group = bpf_map_lookup_elem(&io_latency_group_map, &css);
	if (!group) {
		initial.serial = BPF_CORE_READ(state, serial_nr);
		if (!initial.serial)
			return 0;
		ret = (int)bpf_map_update_elem(&io_latency_group_map, &css,
					      &initial, COMPAT_BPF_NOEXIST);
		if (ret && ret != -IO_LATENCY_EEXIST) {
			io_latency_fail(IO_LATENCY_GROUP_INSERT_FAILED, ret);
			return 0;
		}
		group = bpf_map_lookup_elem(&io_latency_group_map, &css);
	}
	return group ? group->serial : 0;
}

static __always_inline void io_latency_bio_identity(
	struct bio *bio, struct container_latency_key *key)
{
	u64 css, serial;

	if (!io_latency_containers_enabled)
		return;
	css = io_latency_bio_css((u64)bio);
	if (!css)
		return;
	serial = io_latency_css_serial(css);
	if (serial) {
		key->blkcg = css;
		key->serial = serial;
	}
}

SEC("kprobe/blkcg_css_free")
int io_latency_css_released(struct pt_regs *ctx)
{
	struct cgroup_subsys_state *css = (void *)PT_REGS_PARM1_CORE(ctx);
	struct io_latency_group_state *group;
	struct container_latency_key key = { .kind = IO_LATENCY_CSS_RELEASED };
	u64 address = (u64)css;
	int ret;

	group = bpf_map_lookup_elem(&io_latency_group_map, &address);
	if (!group || !group->serial)
		return 0;
	key.blkcg = address;
	key.serial = group->serial;
	ret = (int)bpf_map_update_elem(&blkcg_lat_map, &key,
				      &zero_latency_counters, COMPAT_BPF_NOEXIST);
	if (ret && ret != -IO_LATENCY_EEXIST)
		io_latency_fail(IO_LATENCY_GROUP_RELEASE_FAILED, ret);
	ret = (int)bpf_map_delete_elem(&io_latency_group_map, &address);
	if (ret && ret != -IO_LATENCY_ENOENT)
		io_latency_fail(IO_LATENCY_GROUP_RELEASE_FAILED, ret);
	return 0;
}

/* Keep lookup/create branches in their own verifier call frame. */
static __noinline struct latency_counters *lookup_container_counters(
	const struct container_latency_key *state)
{
	struct container_latency_key key = {
		.blkcg = state->blkcg,
		.serial = state->serial,
		.major = state->major,
		.minor = state->minor,
		.operation = state->operation,
	};
	struct latency_counters *counters;
	int ret;

	if (!state->blkcg || !state->serial)
		return NULL;

	counters = bpf_map_lookup_elem(&blkcg_lat_map, &key);
	if (!counters) {
		ret = bpf_map_update_elem(&blkcg_lat_map, &key,
					  &zero_latency_counters,
					  COMPAT_BPF_NOEXIST);
		if (ret == -IO_LATENCY_EBUSY)
			return NULL;
		counters = bpf_map_lookup_elem(&blkcg_lat_map, &key);
		/* Host accounting is independent of container allocation. Missing
		 * container samples remain included in Host.
		 */
	}
	return counters;
}

static __always_inline int account_io_latency(
	struct latency_counters *host,
	u32 stage, u64 start_ns, u64 end_ns)
{
	u64 bucket = io_latency_bucket(start_ns, end_ns,
				       stage != IO_LATENCY_STAGE_D2C);

	/* Global calls return an unconstrained 64-bit scalar to the verifier.
	 * Bound the whole register before using it as an array index.
	 */
	asm volatile("" : "+r"(bucket));
	if (bucket >= IO_LATENCY_BUCKETS)
		return -1;

	increment_latency(host, stage, bucket);
	return bucket;
}

static __always_inline void increment_io_size(
	struct latency_counters *counters, u32 point, int bucket)
{
	if (point == IO_SIZE_POINT_QUEUE)
		__sync_fetch_and_add(&counters->queued_size[bucket], 1);
	else if (point == IO_SIZE_POINT_ISSUE)
		__sync_fetch_and_add(&counters->issued_size[bucket], 1);
}

struct request_queue___iolatency {
	struct gendisk *disk;
} __attribute__((preserve_access_index));

/* Some kernels keep dispatch size in an allocation prefix. CO-RE supplies
 * both its total size and member offset; rq points immediately after it.
 */
struct request_aux___iolatency {
	u64 alloc_time_ns;
	u16 stats_sectors;
} __attribute__((preserve_access_index));

static __always_inline struct gendisk *io_latency_request_disk(
	struct request *req)
{
	struct request_queue___iolatency *q;

	if (bpf_core_field_exists(req->rq_disk))
		return BPF_CORE_READ(req, rq_disk);
	q = (struct request_queue___iolatency *)BPF_CORE_READ(req, q);
	return BPF_CORE_READ(q, disk);
}

/*
 * Q counts new bio submissions. A counts only a remap whose existing
 * BIO_TRACE_COMPLETION suppresses the destination's Q; partition remaps
 * are excluded separately. The flag belongs to the kernel, not this tracer.
 */
static __noinline u32 queue_bio_latency(
	struct bio *bio, struct gendisk *disk, u64 operation)
{
	struct host_latency_counters *counters;
	struct latency_counters *container;
	u32 bytes;
	int bucket;

	if (operation != REQ_OP_READ && operation != REQ_OP_WRITE)
		return 0;
	counters = lookup_host_counters(disk);
	if (!counters)
		return 0;
	bytes = BPF_CORE_READ(bio, bi_iter.bi_size);
	if (!bytes)
		return 0;
	bucket = io_size_bucket(bytes);
	increment_io_size(&counters->counters[operation],
			  IO_SIZE_POINT_QUEUE, bucket);
	if (!io_latency_containers_enabled)
		return bytes;

	struct container_latency_key series = {
		.major = counters->major,
		.minor = counters->minor,
		.operation = operation,
	};

	io_latency_bio_identity(bio, &series);
	if (!series.blkcg)
		return bytes;
	container = lookup_container_counters(&series);
	if (container)
		increment_io_size(container, IO_SIZE_POINT_QUEUE, bucket);
	return bytes;
}

static __always_inline void queue_bio_on_disk(struct bio *bio,
	struct gendisk *disk)
{
	u32 operation = BPF_CORE_READ(bio, bi_opf) & REQ_OP_MASK;

	queue_bio_latency(bio, disk, operation);
}

SEC("raw_tracepoint/block_bio_queue")
int trace_bio_queue(struct bpf_raw_tracepoint_args *ctx)
{
	struct bio *bio = (struct bio *)io_latency_pointer_argument(
		ctx, block_bio_queue_bio_arg);
	struct {
		void *device;
		u32 operation;
	} submission = {};
	struct gendisk *disk;
	u32 device_offset;

	if (!bio)
		return 0;
	if (bpf_core_field_exists(bio->bi_disk))
		device_offset = compat_bpf_core_field_offset(bio->bi_disk);
	else
		device_offset = compat_bpf_core_field_offset(
			((struct bio___5_12 *)bio)->bi_bdev);

	/* CO-RE resolves these layout checks before execution. Copy the
	 * adjacent device pointer and operation with one probe read.
	 */
	if (compat_bpf_core_field_offset(bio->bi_opf) ==
	    device_offset + sizeof(submission.device)) {
		bpf_core_read(&submission, sizeof(submission),
			      (char *)bio + device_offset);
		if (bpf_core_field_exists(bio->bi_disk)) {
			disk = submission.device;
		} else {
			struct block_device *bdev = submission.device;

			disk = bdev ? BPF_CORE_READ(bdev, bd_disk) : NULL;
		}
		if (disk)
			queue_bio_latency(bio, disk,
				submission.operation & REQ_OP_MASK);
		return 0;
	}
	disk = bio_disk(bio);
	if (disk)
		queue_bio_on_disk(bio, disk);
	return 0;
}

/* Partition translation changes sectors within the same whole-disk series.
 * Its source dev_t names the bio's current partition; DM/MD remaps name the
 * upper device instead, including when the destination is a partition.
 */
static __always_inline struct gendisk *io_latency_remap_disk(
	struct bio *bio, u32 from_dev)
{
	if (bpf_core_field_exists(bio->bi_partno)) {
		struct gendisk *disk = BPF_CORE_READ(bio, bi_disk);
		u8 partno = BPF_CORE_READ(bio, bi_partno);

		if (partno) {
			struct disk_part_tbl *table = BPF_CORE_READ(disk, part_tbl);
			struct hd_struct *part = BPF_CORE_READ(table, part[partno]);

			if (from_dev && from_dev == BPF_CORE_READ(part, __dev.devt))
				return NULL;
		}
		return disk;
	} else {
		struct bio___5_12 *bio_new = (struct bio___5_12 *)bio;
		struct block_device___5_12 *bdev =
			(void *)BPF_CORE_READ(bio_new, bi_bdev);

		if (from_dev && from_dev == BPF_CORE_READ(bdev, bd_dev))
			return NULL;
		return BPF_CORE_READ(bdev, bd_disk);
	}
}

SEC("raw_tracepoint/block_bio_remap")
int trace_bio_remap(struct bpf_raw_tracepoint_args *ctx)
{
	struct bio *bio = (struct bio *)io_latency_pointer_argument(
		ctx, block_bio_remap_bio_arg);
	u64 from_dev;

	/* Keep the loads at fixed ctx offsets, as for the bio argument above. */
	if (block_bio_remap_bio_arg) {
		from_dev = ctx->args[2];
		asm volatile("" : "+r"(from_dev));
	} else {
		from_dev = ctx->args[1];
		asm volatile("" : "+r"(from_dev));
	}

	if (bio && (BPF_CORE_READ(bio, bi_flags) &
		    (1U << io_latency_bio_trace_completion))) {
		struct gendisk *disk = io_latency_remap_disk(bio, from_dev);

		if (disk)
			queue_bio_on_disk(bio, disk);
	}
	return 0;
}

/* Bound each verifier call to 32 bios, as for completion. Only the next bio
 * address crosses chunks; total remap accounting still covers at most 512.
 */
__noinline u64 queue_remapped_bios(u64 address, u64 disk_address,
	u64 operation)
{
	struct bio *bio = (void *)address;
	struct gendisk *disk = (void *)disk_address;

	for (int i = 0; i < IO_LATENCY_REQUEST_CHUNK_BIOS && bio; i++) {
		queue_bio_latency(bio, disk, operation);
		bio = BPF_CORE_READ(bio, bi_next);
	}
	return (u64)bio;
}

SEC("raw_tracepoint/block_rq_remap")
int trace_request_remap(struct bpf_raw_tracepoint_args *ctx)
{
	struct request *req = (struct request *)io_latency_pointer_argument(
		ctx, block_rq_remap_request_arg);
	struct gendisk *disk;
	struct bio *bio;
	u32 operation;

	if (!req)
		return 0;
	disk = io_latency_request_disk(req);
	if (!lookup_host_counters(disk))
		return 0;
	operation = BPF_CORE_READ(req, cmd_flags) & REQ_OP_MASK;
	if ((operation != REQ_OP_READ && operation != REQ_OP_WRITE) ||
	    !BPF_CORE_READ(req, __data_len))
		return 0;
	/* Cloned requests are dispatched directly, without a bio Q. */
	bio = BPF_CORE_READ(req, bio);
	for (int i = 0;
	     i < IO_LATENCY_REQUEST_SCAN_BUDGET / IO_LATENCY_REQUEST_CHUNK_BIOS && bio;
	     i++)
		bio = (void *)queue_remapped_bios((u64)bio, (u64)disk,
					       operation);
	if (bio)
		io_latency_fail(IO_LATENCY_CHAIN_OVERFLOW, 0);
	return 0;
}

/*
 * One completion context holds each shared request/disk read once. The kernel
 * refreshes io_start_time_ns on reissue, so Q2D and D2C use the last D.
 * Keep its plug-cache precision rather than restoring G/D timing probes.
 */
struct request_completion {
	u64 disk;
	u64 operation;
	struct latency_counters *host;
	u64 get_request_ns;
	u64 issue_ns;
	u64 now;
	int d2c_bucket;
};

/*
 * Older bios pack the timestamp into 51 bits. Restore its epoch relative to
 * C before comparing it with full-width request times. A missing/zero start
 * disables Q stages only; the live request still owns D2C and issued size.
 * Split and cloned bios use the kernel's own submission timestamp.
 */
__noinline u64 io_latency_bio_queue_ns(u64 address, u64 now)
{
	struct bio *bio = (void *)address;
	u64 stamp = 0;

	if (bpf_core_field_exists(bio->bi_issue)) {
		const u64 mask = (1ULL << 51) - 1;
		u64 age;

		/* Failed probe reads leave zero, which the missing-stamp check below
		 * already handles. Avoid a redundant branch for every bio in the walk.
		 */
		bpf_core_read(&stamp, sizeof(stamp), &bio->bi_issue);
		stamp &= mask;
		/* Older verifiers explore fallthrough first. Resolve missing Q before
		 * expanding its accounting paths across the 512-bio walk.
		 */
		asm goto("if %0 != 0 goto %l[with_stamp]" : : "r"(stamp) : : with_stamp);
		return 0;
with_stamp:
		age = (now - stamp) & mask;
		return age <= now ? now - age : 0;
	} else {
		struct bio___7_0 *current = (void *)bio;

		if (bpf_core_field_exists(current->issue_time_ns))
			bpf_core_read(&stamp, sizeof(stamp), &current->issue_time_ns);
		return stamp <= now ? stamp : 0;
	}
}

/* All four bucket numbers fit in one byte each; 255 means no sample.
 * Scalar arguments allow independent checking of container lookup/create
 * paths on kernels with global-function verification, without extra reads.
 */
__noinline int account_completed_container(u64 address, u64 disk,
	u64 operation, u64 buckets)
{
	struct container_latency_key series = {
		.major = disk >> 32,
		.minor = disk,
		.operation = operation,
	};
	struct latency_counters *counters;
	u64 q2g = buckets & 255;
	u64 q2d = (buckets >> 8) & 255;
	u64 d2c = (buckets >> 16) & 255;
	u64 size = (buckets >> 24) & 255;

	if (buckets == 0xffffffff)
		return 0;
	io_latency_bio_identity((void *)address, &series);
	counters = lookup_container_counters(&series);
	if (!counters)
		return 0;
	/* Check each decoded register. Comparing the packed word instead loses
	 * the index bound in older verifiers.
	 */
	asm volatile("" : "+r"(q2g));
	if (q2g < IO_LATENCY_BUCKETS)
		increment_latency(counters, IO_LATENCY_STAGE_Q2G, q2g);
	asm volatile("" : "+r"(q2d));
	if (q2d < IO_LATENCY_BUCKETS)
		increment_latency(counters, IO_LATENCY_STAGE_Q2D, q2d);
	asm volatile("" : "+r"(d2c));
	if (d2c < IO_LATENCY_BUCKETS)
		increment_latency(counters, IO_LATENCY_STAGE_D2C, d2c);
	asm volatile("" : "+r"(size));
	if (size < IO_SIZE_BUCKETS)
		increment_io_size(counters, IO_SIZE_POINT_ISSUE, size);
	return 0;
}

static __noinline void complete_bio_latency(
	struct bio *bio, const struct request_completion *completion,
	u64 issued_bytes)
{
	u32 d2c_bucket = completion->d2c_bucket;
	int q2g = -1, q2d = -1, size = -1;
	u64 queue_ns;

	queue_ns = io_latency_bio_queue_ns((u64)bio, completion->now);
	if (issued_bytes) {
		size = io_size_bucket(issued_bytes);
		increment_io_size(completion->host, IO_SIZE_POINT_ISSUE, size);
	}
	asm volatile("" : "+r"(d2c_bucket));
	if (d2c_bucket < IO_LATENCY_BUCKETS) {
		increment_latency(completion->host, IO_LATENCY_STAGE_D2C,
				  d2c_bucket);
	} else {
		d2c_bucket = 255;
	}
	/* Older verifiers inline global checks and explore fallthrough first.
	 * Check missing Q and the optional-container exit before expanding their
	 * paths for every bio; runtime accounting is identical in either order.
	 */
	asm goto("if %0 != 0 goto %l[with_queue]" : : "r"(queue_ns) : : with_queue);
	goto containers;
with_queue:
	q2g = account_io_latency(completion->host, IO_LATENCY_STAGE_Q2G,
		queue_ns, completion->get_request_ns);
	q2d = account_io_latency(completion->host, IO_LATENCY_STAGE_Q2D,
		queue_ns, completion->issue_ns);
containers:
	asm goto("if %0 != 0 goto %l[with_container]" : :
		 "r"((u64)io_latency_containers_enabled) : : with_container);
	return;
with_container:
	{
		u64 buckets = (u8)q2g | (u64)(u8)q2d << 8 |
			(u64)(u8)d2c_bucket << 16 | (u64)(u8)size << 24;

		account_completed_container((u64)bio, completion->disk,
			completion->operation, buckets);
	}
}

struct bio_completion_cursor {
	struct bio *bio;
	u32 remaining;
};

/*
 * Walk at most 32 bios per call to keep load-time verifier work bounded.
 * Only the next bio and this C's remaining bytes cross chunk boundaries.
 * A partial bio ends this C's walk and is accounted when it completes.
 * Consuming all completion bytes also ends the walk.
 */
static __noinline bool complete_bio_chunk(
	const struct request_completion *completion,
	struct bio_completion_cursor *cursor, u32 limit)
{
	struct bio *bio = cursor->bio;
	u32 remaining = cursor->remaining;
	u32 bytes;

	for (u32 i = 0; i < limit && bio; i++) {
		bytes = BPF_CORE_READ(bio, bi_iter.bi_size);
		if (bytes > remaining)
			return false;
		complete_bio_latency(bio, completion, 0);
		remaining -= bytes;
		if (!remaining)
			return false;
		bio = BPF_CORE_READ(bio, bi_next);
	}
	cursor->bio = bio;
	cursor->remaining = remaining;
	return true;
}

/*
 * The raw event precedes advancement of req->bio. Match the kernel's byte
 * walk: account only whole bios and keep a partial bio for its next C.
 * Count request size once at final C, owned by the first remaining bio.
 * NVMe/virtio finish whole requests, so use the event's wide byte count.
 * SCSI may finish in parts: keep its last-D stats_sectors instead of the
 * shrunken remaining length. Retain that field's 16-bit sector limit
 * (32 MiB) rather than add per-request size state or a D probe.
 * Kernels that merge different blkcgs make this container attribution
 * approximate; the host request-size sample is unaffected.
 */
SEC("raw_tracepoint/block_rq_complete")
int trace_request_complete(struct bpf_raw_tracepoint_args *ctx)
{
	struct request *req = (struct request *)ctx->args[0];
	struct request_completion completion = {};
	struct {
		u32 header[8];
		struct bio *head;
		struct bio *tail;
	} bios = {};
	struct gendisk *disk;
	struct host_latency_counters *counters;
	struct bio *bio;
	u64 issued_bytes = 0;
	u64 operation;
	u32 request_bytes = 0;
	u32 remaining;
	u32 bytes;
	struct bio_completion_cursor cursor;

	if (!req)
		return 0;
	remaining = ctx->args[2];
	/* Flush sequencing can restore exhausted bios for a final zero-byte C.
	 * Only the data completion contributes latency and issued-size samples.
	 */
	if (!remaining)
		return 0;
	/* Tags and timeout fields vary between cmd_flags and bio. Read their
	 * small prefix once when all required fields fit; other layouts keep
	 * the individual reads. Every offset below is a CO-RE constant.
	 */
	u32 size_offset = compat_bpf_core_field_offset(req->__data_len) -
		compat_bpf_core_field_offset(req->cmd_flags);
	bool header_snapshot =
		compat_bpf_core_field_offset(req->bio) ==
			compat_bpf_core_field_offset(req->cmd_flags) + sizeof(bios.header) &&
		compat_bpf_core_field_offset(req->biotail) ==
			compat_bpf_core_field_offset(req->bio) + sizeof(bios.head) &&
		size_offset >= sizeof(request_bytes) &&
		size_offset <= sizeof(bios.header) - sizeof(request_bytes);

	if (header_snapshot) {
		bpf_core_read(&bios, sizeof(bios), &req->cmd_flags);
		operation = bios.header[0] & REQ_OP_MASK;
		__builtin_memcpy(&request_bytes, (char *)bios.header + size_offset,
				 sizeof(request_bytes));
	} else {
		operation = BPF_CORE_READ(req, cmd_flags) & REQ_OP_MASK;
	}
	/* Keep the index at register width: older verifiers can lose a u32
	 * bound when LLVM reloads the operation from the completion stack.
	 */
	asm volatile("" : "+r"(operation));
	if (operation != REQ_OP_READ && operation != REQ_OP_WRITE)
		return 0;
	completion.operation = operation;
	disk = io_latency_request_disk(req);
	if (!disk)
		return 0;
	counters = lookup_host_counters(disk);
	if (!counters)
		return 0;
	completion.disk = (u64)counters->major << 32 | counters->minor;
	completion.host = &counters->counters[operation];
	if (!header_snapshot) {
		if (compat_bpf_core_field_offset(req->biotail) ==
		    compat_bpf_core_field_offset(req->bio) + sizeof(bios.head))
			bpf_core_read(&bios.head, sizeof(bios.head) + sizeof(bios.tail),
				      &req->bio);
		else
			bios.head = BPF_CORE_READ(req, bio);
	}
	bio = bios.head;
	if (!bio)
		return 0;
	if (compat_bpf_core_field_offset(req->io_start_time_ns) ==
	    compat_bpf_core_field_offset(req->start_time_ns) +
	    sizeof(completion.get_request_ns)) {
		bpf_core_read(&completion.get_request_ns,
			      sizeof(completion.get_request_ns) + sizeof(completion.issue_ns),
			      &req->start_time_ns);
	} else {
		completion.get_request_ns = BPF_CORE_READ(req, start_time_ns);
		completion.issue_ns = BPF_CORE_READ(req, io_start_time_ns);
	}
	completion.now = bpf_ktime_get_ns();
	/* Every bio in this request shares the last D and this C timestamp. */
	completion.d2c_bucket = io_latency_bucket(completion.issue_ns, completion.now, 0);
	if (!header_snapshot)
		request_bytes = BPF_CORE_READ(req, __data_len);
	if (remaining >= request_bytes && completion.issue_ns) {
		if (counters->scsi) {
			if (bpf_core_field_exists(req->stats_sectors)) {
				issued_bytes = (u64)BPF_CORE_READ(req, stats_sectors) << 9;
			} else {
				struct request_aux___iolatency *aux;
				/* BPF_TYPE_SIZE=1 also works with legacy CO-RE headers. */
				u32 size = __builtin_preserve_type_info(
					*(struct request_aux___iolatency *)0, 1);

				aux = (void *)req - size;
				issued_bytes = (u64)BPF_CORE_READ(aux, stats_sectors) << 9;
			}
		} else {
			issued_bytes = remaining;
		}
	}
	/* A single bio consumes the whole request at its final data completion.
	 * The request byte count then avoids another read of bio->bi_size.
	 */
	if (remaining == request_bytes && bio == bios.tail) {
		complete_bio_latency(bio, &completion, issued_bytes);
		return 0;
	}
	bytes = BPF_CORE_READ(bio, bi_iter.bi_size);
	if (bytes > remaining)
		return 0;
	complete_bio_latency(bio, &completion, issued_bytes);
	remaining -= bytes;
	if (!remaining)
		return 0;
	bio = BPF_CORE_READ(bio, bi_next);
	cursor.bio = bio;
	cursor.remaining = remaining;
	/* The first bio above counts toward 512 and owns the issued-size sample.
	 * The remaining 511 use 15 full chunks and one 31-bio final chunk.
	 */
	for (u32 i = 0; i < IO_LATENCY_COMPLETION_CHUNK_COUNT && cursor.bio; i++) {
		u32 limit = i == IO_LATENCY_COMPLETION_CHUNK_COUNT - 1 ?
			IO_LATENCY_REQUEST_SCAN_BUDGET - 1 -
			(IO_LATENCY_COMPLETION_CHUNK_COUNT - 1) *
			IO_LATENCY_REQUEST_CHUNK_BIOS :
			IO_LATENCY_REQUEST_CHUNK_BIOS;

		if (!complete_bio_chunk(&completion, &cursor, limit))
			return 0;
	}
	if (cursor.bio)
		io_latency_fail(IO_LATENCY_CHAIN_OVERFLOW, 0);
	return 0;
}

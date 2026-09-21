#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_blkio.h"
#include "bpf_common.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define IO_LATENCY_BUCKETS 17
#define IO_SIZE_BUCKETS 6
#define IO_SIZE_KIB 1024ULL
#define IO_LATENCY_REQUEST_SCAN_BUDGET 512
#define IO_LATENCY_COMPLETION_CHUNK_BIOS 32
#define IO_LATENCY_COMPLETION_CHUNK_COUNT \
	((IO_LATENCY_REQUEST_SCAN_BUDGET - 1 + IO_LATENCY_COMPLETION_CHUNK_BIOS - 1) / \
	 IO_LATENCY_COMPLETION_CHUNK_BIOS)
#define IO_LATENCY_BIO_STATES 10240
#define IO_LATENCY_DISKS 128
#define IO_LATENCY_CONTAINER_CSS 2048
#define IO_LATENCY_OPERATIONS 2
#define IO_LATENCY_CONTAINER_SERIES \
	(IO_LATENCY_CONTAINER_CSS * IO_LATENCY_DISKS * \
	 IO_LATENCY_OPERATIONS)
#define IO_LATENCY_CHAIN_OVERFLOW 1
#define IO_LATENCY_STATE_INSERT_FAILED 2
#define IO_LATENCY_STATE_DELETE_FAILED 5
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
volatile const u32 block_split_bio_arg = 0;
volatile const u64 io_latency_root_blkcg = 0;
volatile const bool io_latency_containers_enabled = true;

struct disk_entry {
	u64 disk;
	u32 major;
	u32 minor;
	u64 freeze_nr;
};

struct blkgq_entry {
	u64 disk;
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct disk_entry);
	__uint(max_entries, IO_LATENCY_DISKS);
	__uint(map_flags, COMPAT_BPF_F_NO_PREALLOC);
} blkdisk_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct blkgq_entry);
	__uint(max_entries, IO_LATENCY_CONTAINER_CSS);
} blkcg_map SEC(".maps");

struct bio_latency_state {
	u64 queue_ns;
	u64 blkcg;
	u32 major;
	u32 minor;
};

struct latency_counters {
	u64 q2d[IO_LATENCY_BUCKETS];
	u64 d2c[IO_LATENCY_BUCKETS];
	u64 q2g[IO_LATENCY_BUCKETS];
	u64 queued_size[IO_SIZE_BUCKETS];
	u64 issued_size[IO_SIZE_BUCKETS];
};

static const struct latency_counters zero_latency_counters = {};

/* Userspace retires the whole disk, including its container series. */
struct host_latency_counters {
	u32 major;
	u32 minor;
	u64 retired;
	struct latency_counters counters[IO_LATENCY_OPERATIONS];
};

struct container_latency_key {
	u64 blkcg;
	u32 major;
	u32 minor;
	u32 operation;
	u32 pad;
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct bio_latency_state);
	__uint(max_entries, IO_LATENCY_BIO_STATES);
} bio_latency_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__type(key, u32);
	__type(value, u64);
	/* 0: terminal error; 1: disk configuration change sequence. */
	__uint(max_entries, 2);
} io_latency_status_map SEC(".maps");

/*
 * Keep failure handling out of per-bio accounting to bound older verifier
 * state. Map operations return signed 32-bit errno; consume their results as
 * int so direct JIT calls and helper wrappers use the same comparison width.
 * One aligned word publishes the reason and signed helper errno. Userspace
 * checks it at collection boundaries and detaches the session on error.
 */
static __noinline void io_latency_fail(u32 reason, int error)
{
	u32 key = 0;
	u64 *status;

	/* A nested hash writer can return EBUSY before changing the map.
	 * Skip that write: missed samples and retained bio state are accepted.
	 * Retrying here cannot release the interrupted writer's guard.
	 */
	if (error == -IO_LATENCY_EBUSY)
		return;
	status = bpf_map_lookup_elem(&io_latency_status_map, &key);

	if (status)
		*status = ((u64)reason << 32) | (u32)error;
}

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct host_latency_counters);
	__uint(max_entries, IO_LATENCY_DISKS);
	/* A probe may still use this row after userspace removes admission. */
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
	__uint(max_entries, IO_LATENCY_CONTAINER_SERIES);
	__uint(map_flags, COMPAT_BPF_F_NO_PREALLOC);
} blkcg_lat_map SEC(".maps");

#include "bpf_iolatency_disk.h"

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

/* Keep classification temporaries outside the 512-bio walk's call frame. */
static __noinline int io_latency_bucket(u64 start_ns, u64 end_ns)
{
	u64 ns;
	u32 bucket = 0;

	if (!start_ns || end_ns <= start_ns)
		return -1;
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

/* Keep lookup/create branches in their own verifier call frame. */
static __noinline struct latency_counters *lookup_container_counters(
	const struct container_latency_key *state, struct gendisk *disk)
{
	struct container_latency_key key = {
		.blkcg = state->blkcg,
		.major = state->major,
		.minor = state->minor,
		.operation = state->operation,
	};
	struct latency_counters *counters;
	struct host_latency_counters *host;
	int ret;

	/* The kernel's root blkcg contributes only to host counters. */
	if (!io_latency_containers_enabled || !state->blkcg ||
	    state->blkcg == io_latency_root_blkcg ||
	    !bpf_map_lookup_elem(&blkcg_map, &state->blkcg))
		return NULL;

	counters = bpf_map_lookup_elem(&blkcg_lat_map, &key);
	if (!counters) {
		ret = bpf_map_update_elem(&blkcg_lat_map, &key,
					  &zero_latency_counters,
					  COMPAT_BPF_NOEXIST);
		if (ret == -IO_LATENCY_EBUSY)
			return NULL;
		counters = bpf_map_lookup_elem(&blkcg_lat_map, &key);
		host = lookup_host_counters(disk);
		/* Userspace revokes admission before deleting counters. Recheck
		 * after publication: either its scan sees this row, or this
		 * creator removes the row published after that scan.
		 */
		if (!host || host->major != key.major || host->minor != key.minor ||
		    !bpf_map_lookup_elem(&blkcg_map, &key.blkcg)) {
			/* Userspace also reclaims orphaned rows on its next dump. */
			bpf_map_delete_elem(&blkcg_lat_map, &key);
			return NULL;
		}
		/* Host accounting is independent of container allocation. Missing
		 * container samples remain included in Host.
		 */
	}
	return counters;
}

static __always_inline void account_io_latency(
	struct latency_counters *host, struct latency_counters *container,
	u32 stage, u64 start_ns, u64 end_ns)
{
	int bucket = io_latency_bucket(start_ns, end_ns);

	if ((u32)bucket >= IO_LATENCY_BUCKETS)
		return;

	if (host)
		increment_latency(host, stage, bucket);
	if (container)
		increment_latency(container, stage, bucket);
}

static __always_inline void increment_io_size(
	struct latency_counters *counters, u32 point, int bucket)
{
	if (point == IO_SIZE_POINT_QUEUE)
		__sync_fetch_and_add(&counters->queued_size[bucket], 1);
	else if (point == IO_SIZE_POINT_ISSUE)
		__sync_fetch_and_add(&counters->issued_size[bucket], 1);
}

static __always_inline void account_io_size(
	struct latency_counters *host, struct latency_counters *container,
	u32 point, u64 bytes)
{
	int bucket = io_size_bucket(bytes);

	if (host)
		increment_io_size(host, point, bucket);
	if (container)
		increment_io_size(container, point, bucket);
}

struct request_queue___iolatency {
	struct gendisk *disk;
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

SEC("kprobe/blk_mq_freeze_queue")
int kprobe_freeze_queue(struct pt_regs *ctx)
{
	struct request_queue *q = (struct request_queue *)PT_REGS_PARM1(ctx);
	struct blkcg_gq *blkg	= BPF_CORE_READ(q, root_blkg);
	struct blkgq_entry *blkgq_entry;
	struct disk_entry *entry;

	blkgq_entry = bpf_map_lookup_elem(&blkcg_map, &blkg);
	if (blkgq_entry) {
		entry = bpf_map_lookup_elem(&blkdisk_map, &blkgq_entry->disk);
		if (entry)
			__sync_fetch_and_add(&entry->freeze_nr, 1);
	}

	return 0;
}

/*
 * Q/A use the latest origin on a whole disk and count queued size once.
 * Moving to another disk starts that layer's own state and queued sample.
 * Map pointers stay in this call frame so older verifiers can reuse its
 * checked paths across the request walk. Keep operation at register width
 * so its read/write bounds remain available for the counter offset.
 */
static __noinline u32 queue_bio_latency(
	struct bio *bio, struct gendisk *disk, u64 operation, u64 now)
{
	struct bio_latency_state state = { .queue_ns = now };
	struct container_latency_key series = { .operation = operation };
	struct bio_latency_state *previous;
	struct host_latency_counters *counters;
	struct latency_counters *host;
	struct latency_counters *container;
	u64 bytes;
	u64 key;
	int ret;

	if (operation != REQ_OP_READ && operation != REQ_OP_WRITE)
		return 0;
	counters = lookup_host_counters(disk);
	if (!counters)
		return 0;
	state.major = series.major = counters->major;
	state.minor = series.minor = counters->minor;
	host = &counters->counters[operation];
	key = (u64)bio;
	bytes = BPF_CORE_READ(bio, bi_iter.bi_size);
	if (!bytes) {
		bpf_map_delete_elem(&bio_latency_map, &key);
		return 0;
	}
	if (io_latency_containers_enabled) {
		state.blkcg = (u64)BPF_CORE_READ(bio, bi_blkg, blkcg);
		series.blkcg = state.blkcg;
	}
	previous = bpf_map_lookup_elem(&bio_latency_map, &key);
	if (!previous || !previous->queue_ns ||
	    previous->major != state.major || previous->minor != state.minor) {
		container = lookup_container_counters(&series, disk);
		account_io_size(host, container,
				IO_SIZE_POINT_QUEUE, bytes);
	}
	/* Q/A restarts every phase; size deduplication does not retain old time. */
	if (previous) {
		*previous = state;
		return bytes;
	}
	ret = bpf_map_update_elem(&bio_latency_map, &key, &state,
				  COMPAT_BPF_ANY);
	if (ret)
		io_latency_fail(IO_LATENCY_STATE_INSERT_FAILED, ret);
	return bytes;
}

static __always_inline void queue_bio_on_disk(struct bio *bio,
	struct gendisk *disk, u64 now)
{
	u32 operation = BPF_CORE_READ(bio, bi_opf) & REQ_OP_MASK;

	queue_bio_latency(bio, disk, operation, now);
}

SEC("raw_tracepoint/block_bio_queue")
int trace_bio_queue(struct bpf_raw_tracepoint_args *ctx)
{
	struct bio *bio = (struct bio *)io_latency_pointer_argument(
		ctx, block_bio_queue_bio_arg);

	if (bio) {
		struct gendisk *disk = bio_disk(bio);

		if (disk)
			queue_bio_on_disk(bio, disk, bpf_ktime_get_ns());
	}
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

	if (bio) {
		struct gendisk *disk = io_latency_remap_disk(bio, from_dev);

		if (disk)
			queue_bio_on_disk(bio, disk, bpf_ktime_get_ns());
	}
	return 0;
}

SEC("raw_tracepoint/block_rq_remap")
int trace_request_remap(struct bpf_raw_tracepoint_args *ctx)
{
	struct request *req = (struct request *)io_latency_pointer_argument(
		ctx, block_rq_remap_request_arg);
	struct gendisk *disk;
	struct bio *bio;
	u32 operation;
	u64 now;

	if (!req)
		return 0;
	disk = io_latency_request_disk(req);
	if (!lookup_host_counters(disk))
		return 0;
	operation = BPF_CORE_READ(req, cmd_flags) & REQ_OP_MASK;
	if ((operation != REQ_OP_READ && operation != REQ_OP_WRITE) ||
	    !BPF_CORE_READ(req, __data_len))
		return 0;
	bio = BPF_CORE_READ(req, bio);
	now = bpf_ktime_get_ns();
	for (int i = 0; i < IO_LATENCY_REQUEST_SCAN_BUDGET && bio; i++) {
		queue_bio_latency(bio, disk, operation, now);
		bio = BPF_CORE_READ(bio, bi_next);
	}
	if (bio)
		io_latency_fail(IO_LATENCY_CHAIN_OVERFLOW, 0);
	return 0;
}

SEC("raw_tracepoint/block_split")
int trace_bio_split(struct bpf_raw_tracepoint_args *ctx)
{
	struct bio *bio = (struct bio *)io_latency_pointer_argument(
		ctx, block_split_bio_arg);
	struct bio_latency_state initial = {};
	struct bio_latency_state *parent;
	struct host_latency_counters *host;
	struct gendisk *disk;
	u64 parent_key, key;
	u32 operation;
	int ret;

	if (!bio)
		return 0;
	disk = bio_disk(bio);
	host = lookup_host_counters(disk);
	if (!host)
		return 0;
	parent_key = (u64)BPF_CORE_READ(bio, bi_private);
	parent = bpf_map_lookup_elem(&bio_latency_map, &parent_key);
	if (!parent || !parent->queue_ns)
		return 0;
	operation = BPF_CORE_READ(bio, bi_opf) & REQ_OP_MASK;
	if ((operation != REQ_OP_READ && operation != REQ_OP_WRITE) ||
	    !BPF_CORE_READ(bio, bi_iter.bi_size))
		return 0;
	initial.major = host->major;
	initial.minor = host->minor;
	if (parent->major != initial.major || parent->minor != initial.minor)
		return 0;
	initial.queue_ns = parent->queue_ns;
	if (io_latency_containers_enabled)
		initial.blkcg = (u64)BPF_CORE_READ(bio, bi_blkg, blkcg);
	key = (u64)bio;
	ret = bpf_map_update_elem(&bio_latency_map, &key, &initial,
				  COMPAT_BPF_ANY);
	if (ret)
		io_latency_fail(IO_LATENCY_STATE_INSERT_FAILED, ret);
	return 0;
}

/*
 * One completion context holds each shared request/disk read once. The kernel
 * refreshes io_start_time_ns on reissue, so Q2D and D2C use the last D.
 * blk_time_get_ns may use a plug-cached timestamp; invalid ordering omits only
 * that interval, including a merged bio whose Q follows the request's G.
 */
struct request_completion {
	struct container_latency_key key;
	struct latency_counters *host;
	struct gendisk *disk;
	u64 get_request_ns;
	u64 issue_ns;
	u64 now;
	int d2c_bucket;
};

/*
 * C accounts only bios with Q/A state, reusing their saved owner. Missing
 * completions may retain state until a later Q/A overwrites it; positive
 * stale pairs remain an accepted sampling error. Each interval checks
 * time ordering.
 */
/* Return scalar start data to the accounting frame so map-pointer states
 * do not multiply throughout the histogram calls in the 512-bio walk.
 */
static __noinline bool read_completed_bio(u64 key,
	struct bio_latency_state *completed)
{
	struct bio_latency_state *state;
	int ret;

	state = bpf_map_lookup_elem(&bio_latency_map, &key);
	asm goto("if %0 != 0 goto %l[queued]" : : "r"(state) : : queued);
	return false;
queued:
	*completed = *state;
	ret = bpf_map_delete_elem(&bio_latency_map, &key);
	if (ret && ret != -IO_LATENCY_ENOENT)
		io_latency_fail(IO_LATENCY_STATE_DELETE_FAILED, ret);
	return true;
}

static __noinline void complete_bio_latency(
	struct bio *bio, const struct request_completion *completion,
	u64 issued_bytes)
{
	struct container_latency_key series = completion->key;
	struct bio_latency_state state;
	struct latency_counters *container;
	u64 key = (u64)bio;
	u64 queue_ns;
	u32 d2c_bucket = completion->d2c_bucket;
	bool found;

	found = read_completed_bio(key, &state);
	asm goto("if %0 != 0 goto %l[active]" : : "r"(found) : : active);
	return;
active:
	queue_ns = state.queue_ns;
	series.blkcg = state.blkcg;
	if (state.major != series.major || state.minor != series.minor)
		queue_ns = 0;
	container = lookup_container_counters(&series, completion->disk);
	if (issued_bytes)
		account_io_size(completion->host, container, IO_SIZE_POINT_ISSUE,
				issued_bytes);
	/* Keep one bounded scalar across both host and container increments. */
	asm volatile("" : "+r"(d2c_bucket));
	if (d2c_bucket < IO_LATENCY_BUCKETS) {
		increment_latency(completion->host, IO_LATENCY_STAGE_D2C,
				  d2c_bucket);
		if (container)
			increment_latency(container, IO_LATENCY_STAGE_D2C,
					  d2c_bucket);
	}
	if (queue_ns) {
		account_io_latency(completion->host, container, IO_LATENCY_STAGE_Q2G,
			queue_ns, completion->get_request_ns);
		account_io_latency(completion->host, container, IO_LATENCY_STAGE_Q2D,
			queue_ns, completion->issue_ns);
	}
}

struct bio_completion_cursor {
	struct bio *bio;
	u32 remaining;
};

/*
 * Walk at most 32 bios per call to keep load-time verifier work bounded.
 * Only the next bio and this C's remaining bytes cross chunk boundaries.
 * A partial bio keeps its start record and ends this C's walk. Consuming
 * all completion bytes also ends the walk.
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
 * walk: retire only whole bios and keep a partial bio for its next C.
 * At the final C, stats_sectors supplies the last dispatch size once per
 * request; its owner is this completion's first remaining bio.
 */
SEC("raw_tracepoint/block_rq_complete")
int trace_request_complete(struct bpf_raw_tracepoint_args *ctx)
{
	struct request *req = (struct request *)ctx->args[0];
	struct request_completion completion = {};
	struct {
		struct bio *head;
		struct bio *tail;
	} bios = {};
	struct gendisk *disk;
	struct host_latency_counters *counters;
	struct bio *bio;
	u64 issued_bytes = 0;
	u32 request_bytes;
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
	completion.key.operation = BPF_CORE_READ(req, cmd_flags) & REQ_OP_MASK;
	if (completion.key.operation != REQ_OP_READ &&
	    completion.key.operation != REQ_OP_WRITE)
		return 0;
	disk = io_latency_request_disk(req);
	if (!disk)
		return 0;
	/* Removing this row stops new accounting for this disk. */
	counters = lookup_host_counters(disk);
	if (!counters)
		return 0;
	completion.key.major = counters->major;
	completion.key.minor = counters->minor;
	completion.host = &counters->counters[completion.key.operation];
	completion.disk = disk;
	if (compat_bpf_core_field_offset(req->biotail) ==
	    compat_bpf_core_field_offset(req->bio) + sizeof(bios.head))
		bpf_core_read(&bios, sizeof(bios), &req->bio);
	else
		bios.head = BPF_CORE_READ(req, bio);
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
	completion.d2c_bucket = io_latency_bucket(completion.issue_ns, completion.now);
	request_bytes = BPF_CORE_READ(req, __data_len);
	if (remaining >= request_bytes && completion.issue_ns)
		issued_bytes = (u64)BPF_CORE_READ(req, stats_sectors) << 9;
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
			IO_LATENCY_COMPLETION_CHUNK_BIOS :
			IO_LATENCY_COMPLETION_CHUNK_BIOS;

		if (!complete_bio_chunk(&completion, &cursor, limit))
			return 0;
	}
	if (cursor.bio)
		io_latency_fail(IO_LATENCY_CHAIN_OVERFLOW, 0);
	return 0;
}

/*
 * Clone teardown owns the remaining bio chain until it calls bio_put. Drop
 * those start records before release; queued samples remain accounted for.
 * Normal completion leaves an empty chain, so return before any map work.
 */
SEC("kprobe/blk_rq_unprep_clone")
int kprobe_unprep_clone(struct pt_regs *ctx)
{
	struct request *req = (struct request *)PT_REGS_PARM1_CORE(ctx);
	struct bio *bio = BPF_CORE_READ(req, bio);

	if (!bio)
		return 0;
	for (int i = 0; i < IO_LATENCY_REQUEST_SCAN_BUDGET && bio; i++) {
		u64 key = (u64)bio;
		int ret = bpf_map_delete_elem(&bio_latency_map, &key);

		/* A clone released before an observed D may have no start record. */
		if (ret && ret != -IO_LATENCY_ENOENT &&
		    ret != -IO_LATENCY_EBUSY) {
			io_latency_fail(IO_LATENCY_STATE_DELETE_FAILED, ret);
			return 0;
		}
		bio = BPF_CORE_READ(bio, bi_next);
	}
	if (bio)
		io_latency_fail(IO_LATENCY_CHAIN_OVERFLOW, 0);
	return 0;
}

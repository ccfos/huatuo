#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_blkio.h"
#include "bpf_compat_7_0.h"

#define LATENCY_20MS_NS 20000000
#define LATENCY_30MS_NS 30000000
#define LATENCY_50MS_NS 50000000
#define LATENCY_100MS_NS 100000000
#define LATENCY_200MS_NS 200000000
#define LATENCY_400MS_NS 400000000

char __license[] SEC("license") = "Dual MIT/GPL";

#define LATENCY_ZONE_MAX (6)

struct disk_entry {
	u64 disk;
	u32 major;
	u32 minor;
	u64 freeze_nr;
	u64 q2c_zone[LATENCY_ZONE_MAX];
	u64 d2c_zone[LATENCY_ZONE_MAX];
};

struct blkgq_entry {
	u64 blkgq;
	u64 disk;
	u32 major, minor;
	u64 q2c_zone[LATENCY_ZONE_MAX];
	u64 d2c_zone[LATENCY_ZONE_MAX];
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct disk_entry);
	__uint(max_entries, 128);
} blkdisk_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct blkgq_entry);
	__uint(max_entries, 2048);
} blkcg_map SEC(".maps");

/*
 * key: bio address
 * value: start timestamp
 */
struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, u64);
	__uint(max_entries, 10240);
} bio_start_time SEC(".maps");

static int zone_index(u64 delta)
{
	if (delta < LATENCY_20MS_NS)
		return -1;

	if (delta <= LATENCY_30MS_NS)
		return 0;

	if (delta <= LATENCY_50MS_NS)
		return 1;

	if (delta <= LATENCY_100MS_NS)
		return 2;

	if (delta <= LATENCY_200MS_NS)
		return 3;

	if (delta <= LATENCY_400MS_NS)
		return 4;

	return 5;
}

/**
 * blk_mq_start_request - Start processing a request
 * @rq: Pointer to request to be started
 *
 * Function used by device drivers to notify the block layer that a request
 * is going to be processed now, so blk layer can do proper initializations
 * such as starting the timeout timer.
 */
SEC("kprobe/blk_mq_start_request")
int kprobe_start_request(struct pt_regs *ctx)
{
	struct request *req = (struct request *)PT_REGS_PARM1(ctx);
	struct bio *bio	    = BPF_CORE_READ(req, bio);
	u64 now		    = ktime_ns_mask();

	for (int i = 0; i < 64 && bio; i++) {
		u64 bio_addr = (u64)bio;

		bpf_map_update_elem(&bio_start_time, &bio_addr, &now,
				    COMPAT_BPF_ANY);
		bio = BPF_CORE_READ(bio, bi_next);
	}

	return 0;
}

static __always_inline int q2c_latency_index(struct bio *bio, u64 now)
{
	u64 bi_issue, val;

	{
		struct bio___7_0 *bio7 = (struct bio___7_0 *)bio;
		if (bpf_core_field_exists(bio7->issue_time_ns)) {
			if (bpf_probe_read(&val, sizeof(val), &bio7->issue_time_ns))
				return -1;
		} else if (bpf_probe_read(&val, sizeof(val), &bio->bi_issue)) {
			return -1;
		}
	}
	if (0)
		return -1;

	bi_issue = val & TIMESTAMP_MASK;

	int q2c = now - bi_issue;
	return zone_index(q2c);
}

static __always_inline int d2c_latency_index(struct bio *bio, u64 now)
{
	u64 bi_start, *start_time;
	u64 bio_addr = (u64)bio;

	start_time = bpf_map_lookup_elem(&bio_start_time, &bio_addr);
	if (!start_time)
		return -1;

	bi_start = *start_time;
	bpf_map_delete_elem(&bio_start_time, &bio_addr);

	// That d2c may be negative, it is safe.
	int d2c = now - bi_start;
	return zone_index(d2c);
}

static __always_inline void
blkcg_latency_account(struct bio *bio, int q2c_index, int d2c_index)
{
	struct blkgq_entry *entry;
	u64 css = (u64)BPF_CORE_READ(bio, bi_blkg, blkcg);

	// userspace updates the map.
	entry = bpf_map_lookup_elem(&blkcg_map, &css);
	if (!entry)
		return;

	if (!entry->major) {
		u32 disk_dev[2];

		bio_major_minor_numbers(bio, disk_dev);

		entry->major = disk_dev[0];
		entry->minor = disk_dev[1];
	}

	__sync_fetch_and_add(&entry->q2c_zone[q2c_index], 1);
	__sync_fetch_and_add(&entry->d2c_zone[d2c_index], 1);
}

static __always_inline void
blkdisk_latency_account(struct bio *bio, int q2c_index, int d2c_index)
{
	struct gendisk *disk = bio_disk(bio);
	struct disk_entry *disk_entry;

	disk_entry = bpf_map_lookup_elem(&blkdisk_map, &disk);
	if (disk_entry) {
		__sync_fetch_and_add(&disk_entry->q2c_zone[q2c_index], 1);
		__sync_fetch_and_add(&disk_entry->d2c_zone[d2c_index], 1);
		return;
	}

	/* gendisk.major, gendisk.first_minor */
	u32 disk_dev[2];

	bio_major_minor_numbers(bio, disk_dev);

	struct disk_entry new_entry = {
		.disk	  = (u64)disk,
		.major	  = disk_dev[0],
		.minor	  = disk_dev[1],
		.q2c_zone = {},
		.d2c_zone = {},
	};

	bpf_map_update_elem(&blkdisk_map, &disk, &new_entry, COMPAT_BPF_ANY);
}

SEC("kprobe/__rq_qos_done_bio")
int kprobe_done_bio(struct pt_regs *ctx)
{
	struct bio *bio = (struct bio *)PT_REGS_PARM2(ctx);
	int q2c_index;
	int d2c_index;
	u64 now;

	now = ktime_ns_mask();

	d2c_index = d2c_latency_index(bio, now);
	q2c_index = q2c_latency_index(bio, now);

	if (q2c_index < 0 || d2c_index < 0)
		return 0;

	blkcg_latency_account(bio, q2c_index, d2c_index);
	blkdisk_latency_account(bio, q2c_index, d2c_index);
	return 0;
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

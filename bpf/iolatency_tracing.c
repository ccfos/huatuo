#include "vmlinux.h"
#include "bpf_common.h"
#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#define LATENCY_20MS_NS  20000000
#define LATENCY_30MS_NS  30000000
#define LATENCY_50MS_NS  50000000
#define LATENCY_100MS_NS 100000000
#define LATENCY_200MS_NS 200000000
#define LATENCY_400MS_NS 400000000

char __license[] SEC("license") = "Dual MIT/GPL";

struct disk_entry {
	u64 disk;
	u32 major;
	u32 minor;
	u64 freeze_nr;
	u64 q2c_zone[6];
	u64 d2c_zone[6];
};

struct blkgq_entry {
	u64 blkgq;
	u64 cgroup;
	u64 disk;
	u64 q2c_zone[6];
	u64 d2c_zone[6];
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct disk_entry);
	__uint(max_entries, 128);
} disk_info SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__type(key, u64);
	__type(value, struct blkgq_entry);
	__uint(max_entries, 2048);
} blkgq_info SEC(".maps");

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

#define TIMESTAMP_MASK (((u64)1 << 51) - 1)

/* Local struct definitions for kernel 5.12+ compatibility.
 * In newer kernels, bio->bi_disk was moved to bio->bi_bdev->bd_disk.
 * These local structs with preserve_access_index enable BPF CO-RE to
 * correctly relocate field offsets at load time.
 */
struct block_device___compat {
	struct gendisk *bd_disk;
	u32 bd_dev;
} __attribute__((preserve_access_index));

struct bio___compat {
	struct block_device *bi_bdev;
} __attribute__((preserve_access_index));

static __always_inline struct gendisk *get_bio_disk(struct bio *bio)
{
	struct gendisk *disk = NULL;

	if (bpf_core_field_exists(bio->bi_disk)) {
		BPF_CORE_READ_INTO(&disk, bio, bi_disk);
	} else {
		/* Kernel 5.12+: bio->bi_disk moved to bio->bi_bdev->bd_disk */
		struct bio___compat *bio_new = (struct bio___compat *)bio;
		struct block_device *bdev;

		BPF_CORE_READ_INTO(&bdev, bio_new, bi_bdev);
		if (bdev) {
			BPF_CORE_READ_INTO(&disk, bdev, bd_disk);
		}
	}

	return disk;
}

static __always_inline u8 get_bio_partno(struct bio *bio)
{
	u8 partno = 0;

	if (bpf_core_field_exists(bio->bi_partno)) {
		BPF_CORE_READ_INTO(&partno, bio, bi_partno);
	} else {
		/* Kernel 5.12+: bi_partno moved to bio->bi_bdev->bd_dev */
		struct bio___compat *bio_new = (struct bio___compat *)bio;
		struct block_device___compat *bdev;

		BPF_CORE_READ_INTO(&bdev, bio_new, bi_bdev);
		if (bdev) {
			BPF_CORE_READ_INTO(&partno, bdev, bd_dev);
		}
	}

	return partno;
}

SEC("kprobe/blk_mq_start_request")
int kprobe_start_request(struct pt_regs *ctx)
{
	struct request *req = (struct request *)PT_REGS_PARM1(ctx);
	struct bio *bio;
	u64 now = bpf_ktime_get_ns() & TIMESTAMP_MASK;

	bio = BPF_CORE_READ(req, bio);
	for (int i = 0; i < 64 && bio; i++) {
		u64 bio_addr = (u64)bio;

		bpf_map_update_elem(&bio_start_time, &bio_addr, &now, COMPAT_BPF_ANY);
		bio = BPF_CORE_READ(bio, bi_next);
	}

	return 0;
}

SEC("kprobe/__rq_qos_done_bio")
int kprobe_done_bio(struct pt_regs *ctx)
{
	struct bio *bio = (struct bio *)PT_REGS_PARM2(ctx);
	u64 now = bpf_ktime_get_ns();
	u64 q2c, d2c;
	struct gendisk *disk = NULL;
	int q2c_index, d2c_index;
	u64 val, bi_issue, bi_start;
	u64 bio_addr = (u64)bio;

	/* val=&bio.bi_issue */
	if (bpf_probe_read(&val, sizeof(val), &bio->bi_issue))
		return 0;

	now = now & TIMESTAMP_MASK;
	bi_issue = val & TIMESTAMP_MASK;
	u64 *start_time = bpf_map_lookup_elem(&bio_start_time, &bio_addr);
	if (!start_time)
		return 0;

	bi_start = *start_time & TIMESTAMP_MASK;
	bpf_map_delete_elem(&bio_start_time, &bio_addr);

	if (!bi_issue || (now < bi_issue))
		q2c = 0;
	else
		q2c = now - bi_issue;
	q2c_index = zone_index(q2c);

	if (!bi_start || (now < bi_start) || (bi_start < bi_issue))
		d2c = 0;
	else
		d2c = now - bi_start;
	d2c_index = zone_index(d2c);

	if ((q2c_index < 0) && (d2c_index < 0))
		return 0;

	struct blkgq_entry *blkgq_entry;
	u64 css;

	css = (u64)BPF_CORE_READ(bio, bi_blkg, blkcg);
	blkgq_entry = bpf_map_lookup_elem(&blkgq_info, &css);
	if (blkgq_entry) {
		if (q2c_index >= 0)
			__sync_fetch_and_add(&blkgq_entry->q2c_zone[q2c_index], 1);
		if (d2c_index >= 0)
			__sync_fetch_and_add(&blkgq_entry->d2c_zone[d2c_index], 1);
	}

	struct disk_entry *disk_entry;

	disk = get_bio_disk(bio);
	disk_entry = bpf_map_lookup_elem(&disk_info, &disk);
	if (disk_entry) {
		if (q2c_index >= 0)
			__sync_fetch_and_add(&disk_entry->q2c_zone[q2c_index], 1);
		if (d2c_index >= 0)
			__sync_fetch_and_add(&disk_entry->d2c_zone[d2c_index], 1);
	} else {
		struct disk_entry new_entry = {.disk = (u64)disk};
		u32 disk_dev[2];
		u8 partno;

		/* gendisk.major, gendisk.first_minor */
		bpf_probe_read(disk_dev, sizeof(disk_dev), disk);
		partno = get_bio_partno(bio);
		new_entry.major = disk_dev[0];
		new_entry.minor = disk_dev[1] + partno;

		if (q2c_index >= 0)
			new_entry.q2c_zone[q2c_index] = 1;
		if (d2c_index >= 0)
			new_entry.d2c_zone[d2c_index] = 1;
		bpf_map_update_elem(&disk_info, &disk, &new_entry, COMPAT_BPF_ANY);
	}

	return 0;
}

SEC("kprobe/blk_mq_freeze_queue")
int kprobe_freeze_queue(struct pt_regs *ctx)
{
	struct request_queue *q = (struct request_queue *)PT_REGS_PARM1(ctx);
	struct blkcg_gq *blkg;
	struct blkgq_entry *blkgq_entry;
	struct disk_entry *disk_entry;

	blkg = BPF_CORE_READ(q, root_blkg);
	blkgq_entry = bpf_map_lookup_elem(&blkgq_info, &blkg);
	if (blkgq_entry) {
		disk_entry = bpf_map_lookup_elem(&disk_info, &blkgq_entry->disk);
		if (disk_entry)
			__sync_fetch_and_add(&disk_entry->freeze_nr, 1);
	}

	return 0;
}

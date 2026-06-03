#ifndef __BPF_FUNC_TRACE_H__
#define __BPF_FUNC_TRACE_H__

#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

#define TIMESTAMP_MASK (((u64)1 << 51) - 1)

static __always_inline u64 ktime_ns_mask()
{
	return bpf_ktime_get_ns() & TIMESTAMP_MASK;
}

/* Local struct definitions for CO-RE compatibility across kernel versions */
struct block_device___compat {
	struct gendisk *bd_disk;
	u32 bd_dev;
} __attribute__((preserve_access_index));

struct bio___compat {
	struct block_device *bi_bdev;
} __attribute__((preserve_access_index));

static __always_inline struct gendisk *bio_disk(struct bio *bio)
{
	struct gendisk *disk = NULL;

	/* kernel 7.0+: bi_disk removed, use bi_bdev->bd_disk */
	if (bpf_core_field_exists(bio->bi_bdev)) {
		struct bio___compat *bio_new = (struct bio___compat *)bio;
		struct block_device *bdev;

		BPF_CORE_READ_INTO(&bdev, bio_new, bi_bdev);
		if (bdev) {
			BPF_CORE_READ_INTO(&disk, bdev, bd_disk);
		}
	}

	return disk;
}

static __always_inline u8 bio_partno(struct bio *bio)
{
	u8 partno = 0;

	/* kernel 7.0+: bi_partno removed, extract from bi_bdev->bd_dev */
	if (bpf_core_field_exists(bio->bi_bdev)) {
		struct bio___compat *bio_new = (struct bio___compat *)bio;
		struct block_device *bdev;

		BPF_CORE_READ_INTO(&bdev, bio_new, bi_bdev);
		if (bdev) {
			dev_t dev = BPF_CORE_READ(bdev, bd_dev);
			partno = (dev & 0xff) | ((dev >> 12) << 8);
		}
	}

	return partno;
}

static __always_inline void
bio_major_minor_numbers(struct bio *bio, u32 *disk_dev)
{
	struct gendisk *disk = bio_disk(bio);

	bpf_probe_read(disk_dev, 2 * sizeof(u32), disk);
	disk_dev[1] = disk_dev[1] + bio_partno(bio);
}

#endif

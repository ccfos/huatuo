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

/* Local struct definitions for kernel 5.12+ compatibility.
 * In newer kernels, bio->bi_disk was moved to bio->bi_bdev->bd_disk.
 * These local structs with preserve_access_index enable BPF CO-RE to
 * correctly relocate field offsets at load time.
 */
struct bio___5_12 {
	struct block_device *bi_bdev;
} __attribute__((preserve_access_index));

static __always_inline struct gendisk *bio_disk(struct bio *bio)
{
	struct gendisk *disk = NULL;

	if (bpf_core_field_exists(bio->bi_disk)) {
		BPF_CORE_READ_INTO(&disk, bio, bi_disk);
	} else {
		/* Kernel 5.12+: bio->bi_disk moved to bio->bi_bdev->bd_disk */
		struct bio___5_12 *bio_new = (struct bio___5_12 *)bio;
		struct block_device *bdev;

		BPF_CORE_READ_INTO(&bdev, bio_new, bi_bdev);
		if (bdev) {
			BPF_CORE_READ_INTO(&disk, bdev, bd_disk);
		}
	}

	return disk;
}

static __always_inline void
bio_major_minor_numbers(struct bio *bio, u32 *disk_dev)
{
	if (bpf_core_field_exists(bio->bi_partno)) {
		struct gendisk *disk = bio_disk(bio);

		bpf_probe_read(disk_dev, 2 * sizeof(u32), disk);
		disk_dev[1] += BPF_CORE_READ(bio, bi_partno);
	} else {
		struct bio___5_12 *bio_new = (struct bio___5_12 *)bio;
		dev_t dev = BPF_CORE_READ(bio_new, bi_bdev, bd_dev);

		/* bd_dev already includes the disk's base minor. */
		disk_dev[0] = dev >> 20;
		disk_dev[1] = dev & ((1U << 20) - 1);
	}
}

#endif

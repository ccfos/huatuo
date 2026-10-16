#ifndef __BPF_FUNC_TRACE_H__
#define __BPF_FUNC_TRACE_H__

#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>

/* Local struct definitions for kernel 5.12+ compatibility.
 * In newer kernels, bio->bi_disk was moved to bio->bi_bdev->bd_disk.
 * These local structs with preserve_access_index enable BPF CO-RE to
 * correctly relocate field offsets at load time.
 */
struct block_device___5_12 {
	struct gendisk *bd_disk;
	u32 bd_dev;
} __attribute__((preserve_access_index));

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

#endif

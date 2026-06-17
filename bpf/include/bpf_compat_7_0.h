#ifndef __BPF_COMPAT_7_0_H__
#define __BPF_COMPAT_7_0_H__

#include "vmlinux.h"
#include <bpf/bpf_core_read.h>

/*
 * CO-RE compat structs for kernel 7.0+ field renames/relocations that
 * cannot be expressed by accessing the field directly on the original
 * kernel type. Each struct only contains the field(s) that the
 * corresponding BPF program needs to read on a 7.0+ kernel; the older
 * field paths are still accessed through the original kernel types
 * (or the existing per-file ___X_Y compat structs).
 */

/*
 * iotracing: rq_disk was removed; gendisk is now reached via
 * request->part->bd_disk on 7.0+.
 */
struct request___7_0 {
	struct block_device *part;
} __attribute__((preserve_access_index));

struct block_device___7_0 {
	struct gendisk *bd_disk;
} __attribute__((preserve_access_index));

/*
 * iotracing: iov_iter::data_source was renamed to ::iter_type on 7.0+.
 */
struct iov_iter___7_0 {
	u8 iter_type;
} __attribute__((preserve_access_index));

/*
 * iolatency_tracing: bio::bi_issue (struct bio_issue with packed
 * timestamp) was replaced by a plain bio::issue_time_ns on 7.0+.
 */
struct bio___7_0 {
	u64 issue_time_ns;
} __attribute__((preserve_access_index));

/*
 * hungtask: trace_event_raw_sched_process_hang::comm changed from a
 * fixed-size __array to a __data_loc string on 7.0+.
 */
struct trace_event_raw_sched_process_hang___7_0 {
	struct trace_entry ent;
	u32 __data_loc_comm;
	pid_t pid;
} __attribute__((preserve_access_index));

#endif /* __BPF_COMPAT_7_0_H__ */

#ifndef __BPF_COMPAT_7_0_H__
#define __BPF_COMPAT_7_0_H__

#include "vmlinux.h"
#include <bpf/bpf_core_read.h>

/* Compat structs for kernel 7.0+ API changes */

struct request___7_0 {
	struct request_queue *q;
	struct block_device *part;
} __attribute__((preserve_access_index));

struct block_device___7_0 {
	struct gendisk *bd_disk;
	dev_t bd_dev;
} __attribute__((preserve_access_index));

struct iov_iter___7_0 {
	u8 iter_type;
	bool data_source;
} __attribute__((preserve_access_index));

struct task_struct___7_0 {
	unsigned int __state;
} __attribute__((preserve_access_index));

struct sock___7_0 {
	u32 sk_protocol;
	u16 sk_type;
} __attribute__((preserve_access_index));

struct trace_event_raw_sched_process_hang___7_0 {
	struct trace_entry ent;
	u32 __data_loc_comm;
	pid_t pid;
	char __data[0];
} __attribute__((preserve_access_index));

struct bio___7_0 {
	struct block_device *bi_bdev;
	u64 issue_time_ns;
} __attribute__((preserve_access_index));

#endif /* __BPF_COMPAT_7_0_H__ */

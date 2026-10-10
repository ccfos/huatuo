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

/* Disk registration and retirement are cold paths. A sysfs-owned disk
 * reference protects enrollment; userspace owns per-disk counter cleanup.
 */
#ifndef HUATUO_BPF_IOLATENCY_DISK_H
#define HUATUO_BPF_IOLATENCY_DISK_H

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
/* disk_release and queue sysfs share the same device-to-disk ownership. */
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
	__u64 disk;
	__u64 publish;
	__u64 registered;
	struct host_latency_counters initial;
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 1);
	__type(key, struct queue_probe_key);
	__type(value, struct queue_probe_result);
	/* Replacing a command leaves in-flight readers on the old value. */
	__uint(map_flags, COMPAT_BPF_F_NO_PREALLOC);
} io_latency_queue_probe SEC(".maps");

/* Each requested read of queue/iostats also samples the state which controls
 * QUEUE_FLAG_STATS: accounting enabled or at least one stats callback.
 * This follows blk-stat.c without depending on flag bit numbers. G is the
 * iostats value returned by sysfs. Results: 1 skipped, 2 eligible,
 * 3 disabled, 4 retired, 5 registered, 6 changed; negative values are errno.
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
	struct queue_probe_result *probe;
	struct host_latency_counters *host;
	struct disk_entry metadata = {};
	__u64 disk_key;
	const char *disk_name, *attr_name;
	struct queue_probe_key key = {};
	char attribute[9] = {};
	__u32 major, minor;
	int accounting = 0, ret;
	bool enabled = false;

	/* Any reader of the requested disk's iostats can finish the probe.
	 * Publication uses NOEXIST, so duplicate reads retain existing counters.
	 */
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
	probe = bpf_map_lookup_elem(&io_latency_queue_probe, &key);
	if (!probe || probe->result)
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
			probe->result = 1;
			return 0;
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
	/* The second sysfs read publishes only the identity checked by Go.
	 * Both publication and retirement execute while the kernel owns disk.
	 */
	if (probe->publish && (probe->disk != disk_key ||
	    probe->initial.major != major || probe->initial.minor != minor)) {
		probe->result = 6;
		return 0;
	}
	probe->initial.major = major;
	probe->initial.minor = minor;
	probe->disk = disk_key;
	host = bpf_map_lookup_elem(&blkdisk_lat_map, &disk_key);
	probe->registered = host != NULL;
	if (host && host->retired) {
		probe->result = 4;
		return 0;
	}
	if (!enabled && next == &stats->callbacks) {
		probe->result = 3;
		return 0;
	}
	/* A concurrent publisher may have created only the Host row so far. */
	if (host && !probe->publish &&
	    bpf_map_lookup_elem(&blkdisk_map, &disk_key)) {
		probe->result = 5;
		return 0;
	}
	if (probe->publish) {
		/* Go supplies a zero counter template in this command map because
		 * the complete Host value exceeds the BPF stack size.
		 */
		ret = (int)bpf_map_update_elem(&blkdisk_lat_map, &disk_key,
					      &probe->initial, COMPAT_BPF_NOEXIST);
		if (ret && ret != -IO_LATENCY_EEXIST)
			goto failed;
		metadata.disk = disk_key;
		metadata.major = major;
		metadata.minor = minor;
		ret = (int)bpf_map_update_elem(&blkdisk_map, &disk_key,
					      &metadata, COMPAT_BPF_NOEXIST);
		if (ret && ret != -IO_LATENCY_EEXIST)
			goto failed;
	}
	probe->result = host && probe->publish ? 5 : 2;
	return 0;
failed:
	probe->result = ret;
	return 0;
}

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__type(key, __u32);
	__type(value, __u32);
} io_latency_disk_events SEC(".maps");

/* Cold-path notifications wake disk discovery after registration or a queue
 * configuration change. The sequence is also read with normal health checks,
 * so a lost notification is recovered at the next collection boundary.
 */
static __always_inline int io_latency_disk_changed(struct pt_regs *ctx)
{
	__u32 key = 1;
	__u64 *changes;

	changes = bpf_map_lookup_elem(&io_latency_status_map, &key);
	if (changes)
		__sync_fetch_and_add(changes, 1);
	bpf_perf_event_output(ctx, &io_latency_disk_events, COMPAT_BPF_F_CURRENT_CPU,
			      &key, sizeof(key));
	return 0;
}

/* Registration returns after default WBT initialization. Userspace still
 * checks the resulting timestamps: successful registration is not a promise
 * that either timestamp is enabled.
 */
SEC("kretprobe/blk_register_queue")
int kretprobe_register_queue(struct pt_regs *ctx)
{
	return PT_REGS_RC(ctx) ? 0 : io_latency_disk_changed(ctx);
}

/* The outer store returns after queue-limit updates have been committed. */
SEC("kretprobe/queue_attr_store")
int kretprobe_queue_config(struct pt_regs *ctx)
{
	return (__s64)PT_REGS_RC(ctx) < 0 ? 0 : io_latency_disk_changed(ctx);
}

/* Attached to callback add/remove and accounting enable/disable. These
 * functions maintain request timestamp collection, including cgroup IOCOST
 * and scheduler changes that do not pass through queue sysfs writes.
 */
SEC("kretprobe/blk_stat_enable_accounting")
int kretprobe_stats_config(struct pt_regs *ctx)
{
	return io_latency_disk_changed(ctx);
}

/* Final disk reference release precedes address reuse. Q/C may continue
 * writing a retained row; userspace discards this disk's transition interval
 * and removes its Host admission before reclaiming subordinate counters.
 */
SEC("kprobe/disk_release")
int kprobe_disk_release(struct pt_regs *ctx)
{
	struct device *device = (struct device *)PT_REGS_PARM1_CORE(ctx);
	struct gendisk *disk = io_latency_device_disk(device);
	__u64 key = (__u64)disk;
	struct host_latency_counters *host;

	host = bpf_map_lookup_elem(&blkdisk_lat_map, &key);
	if (host)
		host->retired = 1;
	/* Freeze counts follow disk lifetime, including while IO timestamps
	 * are disabled and userspace has removed the Host latency entry.
	 */
	bpf_map_delete_elem(&blkdisk_map, &key);
	return 0;
}

#endif

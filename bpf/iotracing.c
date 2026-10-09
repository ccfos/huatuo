#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"
#include "bpf_compat_7_0.h"
#include "abi/iotracing_types.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define FILEPATH_MAX_DEPTH 8
#define DNAME_INLINE_LEN   32
#define PAGE_SIZE	   4096
#define FILTER_DEV_MAX	   16

/* Linux UAPI errno-base.h; libc headers are unsuitable for the BPF target. */
#define E2BIG 7

#ifndef bpf_core_field_offset
#define bpf_core_field_offset(field) \
	__builtin_preserve_field_info(field, BPF_FIELD_BYTE_OFFSET)
#endif

volatile const u32 FILTER_DEV_IDS[FILTER_DEV_MAX] = {};
volatile const u32 FILTER_DEV_COUNT = 0;
volatile const u64 FILTER_EVENT_TIMEOUT = 100000000;
/* Selected from request.part's BTF pointee before the object is loaded. */
volatile const bool REQUEST_PART_BLOCK_DEVICE = false;

static __always_inline int should_process_device(u32 dev)
{
	int i;

	if (!FILTER_DEV_COUNT)
		return 1;

	for (i = 0; i < FILTER_DEV_COUNT && i < FILTER_DEV_MAX; i++)
		if (FILTER_DEV_IDS[i] == dev)
			return 1;

	return 0;
}

struct latency_info {
	u64 cnt;
	u64 max_d2c;
	u64 sum_d2c;
	u64 max_q2c;
	u64 sum_q2c;
};

struct io_key {
	u32 tgid;
	u32 dev;
	u64 inode;
};

struct hash_key {
	dev_t dev;
	u32 _pad;
	sector_t sector;
};

struct io_start_info {
	u64 inode;
	u32 tgid;
	u32 dev;
	u64 data_len;
	struct blkcg_gq *bi_blkg;
	char comm[COMPAT_TASK_COMM_LEN];
};

struct io_data {
	u32 tgid;
	/*
	 * Block completion can create the entry before a pathname is available.
	 */
	u32 path_initialized;
	u32 dev;
	u32 flag;
	u64 fs_write_bytes;
	u64 fs_read_bytes;
	u64 block_write_bytes;
	u64 block_read_bytes;
	u64 inode;
	u64 blkcg_gq;
	struct latency_info latency;
	char comm[COMPAT_TASK_COMM_LEN];
	char filepath[FILEPATH_MAX_DEPTH][DNAME_INLINE_LEN];
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 512);
	__uint(key_size, sizeof(struct io_key));
	__uint(value_size, sizeof(struct io_data));
} io_source_map SEC(".maps");

/* Remember a real capacity failure so unseen files skip path collection and
 * unsuccessful inserts. Existing records still receive updates. Records are
 * never deleted during a capture, so full remains valid until this BPF object
 * is closed; a new capture starts with full cleared.
 */
struct {
	__uint(type, BPF_MAP_TYPE_ARRAY);
	__uint(max_entries, 1);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(u32));
} io_source_full SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 4096);
	__uint(key_size, sizeof(struct hash_key));
	__uint(value_size, sizeof(struct io_start_info));
} start_info_map SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(max_entries, 128);
	__uint(key_size, sizeof(u64));
	__uint(value_size, sizeof(u64));
} request_struct_map SEC(".maps");

#define REQ_OP_BITS 8
#define REQ_OP_MASK ((1U << REQ_OP_BITS) - 1)
#define REQ_META    (1ULL << __REQ_META)

static __always_inline int is_write_request(u32 cmd_flags)
{
	return (cmd_flags & REQ_OP_MASK) == REQ_OP_WRITE;
}

/* Use the partition's dev_t unchanged on both request hooks. */
static __always_inline bool get_request_dev(struct request *req, dev_t *dev)
{
	void *part = NULL;
	struct hd_struct *old_part;
	struct block_device___7_0 *bdev7;

	/* part is not initialized when request IO statistics are disabled. */
	if (BPF_CORE_READ_INTO(&part, req, part) || !part)
		return false;

	old_part = (struct hd_struct *)part;
	if (!REQUEST_PART_BLOCK_DEVICE)
		return BPF_CORE_READ_INTO(dev, old_part, __dev.devt) == 0;

	bdev7 = (struct block_device___7_0 *)part;
	return BPF_CORE_READ_INTO(dev, bdev7, bd_dev) == 0;
}

SEC("kprobe/rq_qos_issue")
int bpf_rq_qos_issue(struct pt_regs *ctx)
{
	struct request *req = (struct request *)PT_REGS_PARM2(ctx);
	struct hash_key key = {};
	struct io_start_info info = {};
	struct bio *bio;
	struct inode *inode;
	u32 cmd_flags;

	bio = BPF_CORE_READ(req, bio);

	cmd_flags = BPF_CORE_READ(req, cmd_flags);
	if (cmd_flags & REQ_META)
		return 0;

	if (!get_request_dev(req, &key.dev))
		return -1;

	key.sector = BPF_CORE_READ(req, __sector);

	if (!should_process_device(key.dev))
		return 0;

	inode = BPF_CORE_READ(bio, bi_io_vec, bv_page, mapping, host);
	info.inode = BPF_CORE_READ(inode, i_ino);
	if (!info.inode)
		info.dev = key.dev;
	else
		info.dev = BPF_CORE_READ(inode, i_sb, s_dev);

	info.tgid = bpf_get_current_pid_tgid() >> 32;
	info.bi_blkg = BPF_CORE_READ(bio, bi_blkg);
	info.data_len = BPF_CORE_READ(req, __data_len);
	bpf_get_current_comm(info.comm, COMPAT_TASK_COMM_LEN);
	bpf_map_update_elem(&start_info_map, &key, &info, COMPAT_BPF_ANY);

	return 0;
}

SEC("kprobe/rq_qos_done")
int bpf_rq_qos_done(struct pt_regs *ctx)
{
	struct request *req = (struct request *)PT_REGS_PARM2(ctx);
	struct io_start_info *info;
	struct hash_key info_key = {};
	struct io_key io_key = {};
	struct io_data data = {};
	struct io_data *entry;
	u32 cmd_flags;
	u64 now;
	u64 q2c;
	u64 d2c;

	if (!get_request_dev(req, &info_key.dev))
		return -1;

	info_key.sector = BPF_CORE_READ(req, __sector);

	if (!should_process_device(info_key.dev))
		return 0;

	info = bpf_map_lookup_elem(&start_info_map, &info_key);
	if (!info)
		return 0;

	io_key.dev = info->dev;
	io_key.inode = info->inode;
	/* Direct IO has no inode, so keep process attribution in the key. */
	if (!io_key.inode)
		io_key.tgid = info->tgid;

	entry = bpf_map_lookup_elem(&io_source_map, &io_key);
	if (!entry)
		entry = &data;

	cmd_flags = BPF_CORE_READ(req, cmd_flags);
	if (is_write_request(cmd_flags)) {
		entry->block_write_bytes += info->data_len;
	} else if ((cmd_flags & REQ_OP_MASK) == REQ_OP_READ) {
		entry->block_read_bytes += info->data_len;
	} else {
		bpf_map_delete_elem(&start_info_map, &info_key);
		return 0;
	}

	now = bpf_ktime_get_ns();
	q2c = now - BPF_CORE_READ(req, start_time_ns);
	d2c = now - BPF_CORE_READ(req, io_start_time_ns);

	entry->latency.sum_q2c += q2c;
	entry->latency.sum_d2c += d2c;
	if (q2c > entry->latency.max_q2c)
		entry->latency.max_q2c = q2c;
	if (d2c > entry->latency.max_d2c)
		entry->latency.max_d2c = d2c;
	entry->latency.cnt++;

	if (entry == &data) {
		entry->blkcg_gq = (u64)info->bi_blkg;
		entry->tgid = info->tgid;
		entry->dev = info->dev;
		entry->inode = info->inode;
		bpf_probe_read_kernel_str(entry->comm, COMPAT_TASK_COMM_LEN,
					  info->comm);
		bpf_map_update_elem(&io_source_map, &io_key, &data,
				    COMPAT_BPF_ANY);
	}
	bpf_map_delete_elem(&start_info_map, &info_key);

	return 0;
}

static __always_inline void
init_io_data(struct io_data *entry, struct dentry *dentry, u32 dev, u64 inode)
{
	/* Consume identity before the path walk to bound the BPF call stack. */
	entry->dev = dev;
	entry->inode = inode;
	entry->tgid = bpf_get_current_pid_tgid() >> 32;

	bpf_get_current_comm(entry->comm, COMPAT_TASK_COMM_LEN);
	for (int i = 0; i < FILEPATH_MAX_DEPTH; i++) {
		if (!dentry)
			break;

		entry->filepath[i][0] = 0;
		bpf_probe_read_kernel_str(entry->filepath[i], DNAME_INLINE_LEN,
					  BPF_CORE_READ(dentry, d_name.name));
		if (!entry->filepath[i][0])
			break;
		if (entry->filepath[i][DNAME_INLINE_LEN - 2] != 0) {
			entry->filepath[i][DNAME_INLINE_LEN - 2] = '.';
			entry->filepath[i][DNAME_INLINE_LEN - 3] = '.';
			entry->filepath[i][DNAME_INLINE_LEN - 4] = '.';
		}
		dentry = BPF_CORE_READ(dentry, d_parent);
	}
	entry->path_initialized = 1;
}

/* Keep the 376-byte initialization and path walk off the map-hit path. */
static __noinline struct io_data *
create_file_io_entry(struct io_key *key, struct file *file, u32 *full)
{
	struct io_data data = {};
	int err;

	init_io_data(&data, BPF_CORE_READ(file, f_path.dentry), key->dev, key->inode);
	err = bpf_map_update_elem(&io_source_map, key, &data,
				  COMPAT_BPF_NOEXIST);
	if (err == -E2BIG && full)
		*full = 1;

	/* A concurrent first observation may already have installed this key. */
	return bpf_map_lookup_elem(&io_source_map, key);
}

static __always_inline struct io_data *
get_file_io_entry(struct io_key *key, struct file *file)
{
	struct io_data *entry = bpf_map_lookup_elem(&io_source_map, key);

	if (!entry) {
		u32 zero = 0;
		u32 *full = bpf_map_lookup_elem(&io_source_full, &zero);

		if (full && *full)
			return NULL;
		entry = create_file_io_entry(key, file, full);
		if (!entry)
			return NULL;
	}
	/* Block completion can create an entry before its first file event. */
	if (!entry->path_initialized)
		init_io_data(entry, BPF_CORE_READ(file, f_path.dentry), key->dev, key->inode);
	return entry;
}

static __always_inline int bpf_file_read_write(struct pt_regs *ctx, bool is_write)
{
	struct kiocb *iocb    = (struct kiocb *)PT_REGS_PARM1(ctx);
	struct io_data *entry = NULL;
	struct file *file;
	struct inode *inode;
	struct io_key key = {};
	struct iov_iter *from;
	size_t count;
	u32 io_flags = 0;
	u64 inode_number = 0;

	/* Batch only when the target layout matches the snapshot's field spacing. */
	bool bulk_iocb = bpf_core_field_offset(iocb->ki_flags) -
		bpf_core_field_offset(iocb->ki_filp) == 32;

	if (bulk_iocb) {
		struct {
			struct file *file;
			u64 skipped[3];
			u32 flags;
		} __attribute__((packed)) snapshot;

		BPF_CORE_READ_INTO(&snapshot, iocb, ki_filp);
		file = snapshot.file;
		io_flags = snapshot.flags;
	} else {
		file = BPF_CORE_READ(iocb, ki_filp);
	}
	inode	  = BPF_CORE_READ(file, f_inode);

	bool bulk_inode = bpf_core_field_offset(inode->i_ino) -
		bpf_core_field_offset(inode->i_sb) == 24;

	if (bulk_inode) {
		struct {
			struct super_block *sb;
			u64 skipped[2];
			u64 ino;
		} snapshot;
		struct super_block *sb;

		BPF_CORE_READ_INTO(&snapshot, inode, i_sb);
		sb = snapshot.sb;
		key.dev = BPF_CORE_READ(sb, s_dev);
		inode_number = snapshot.ino;
	} else {
		key.dev = BPF_CORE_READ(inode, i_sb, s_dev);
	}

	if (!should_process_device(key.dev))
		return 0;

	key.inode = bulk_inode ? inode_number : BPF_CORE_READ(inode, i_ino);
	entry = get_file_io_entry(&key, file);
	if (!entry)
		return 0;

	from = (struct iov_iter *)PT_REGS_PARM2(ctx);
	count = BPF_CORE_READ(from, count);

	if (is_write)
		entry->fs_write_bytes += count;
	else
		entry->fs_read_bytes += count;

	entry->flag = bulk_iocb ? io_flags : BPF_CORE_READ(iocb, ki_flags);

	return 0;
}

SEC("kprobe/anyfs_file_read_iter")
int bpf_anyfs_file_read_iter(struct pt_regs *ctx)
{
	return bpf_file_read_write(ctx, false);
}

SEC("kprobe/anyfs_file_write_iter")
int bpf_anyfs_file_write_iter(struct pt_regs *ctx)
{
	return bpf_file_read_write(ctx, true);
}

static __always_inline int bpf_filemap_page_mkwrite(struct pt_regs *ctx)
{
	struct vm_fault *vm = (struct vm_fault *)PT_REGS_PARM1(ctx);
	struct vm_area_struct *vma = BPF_CORE_READ(vm, vma);
	struct io_data *entry	   = NULL;
	struct io_key key	   = {};
	struct file *file;
	struct inode *inode;

	file	  = BPF_CORE_READ(vma, vm_file);
	inode	  = BPF_CORE_READ(file, f_inode);
	key.dev	  = BPF_CORE_READ(inode, i_sb, s_dev);

	if (!should_process_device(key.dev))
		return 0;

	key.inode = BPF_CORE_READ(inode, i_ino);
	entry = get_file_io_entry(&key, file);
	if (!entry)
		return 0;

	entry->fs_write_bytes += PAGE_SIZE;

	return 0;
}

SEC("kprobe/anyfs_filemap_page_mkwrite")
int bpf_anyfs_filemap_page_mkwrite(struct pt_regs *ctx)
{
	return bpf_filemap_page_mkwrite(ctx);
}

SEC("kprobe/filemap_fault")
int bpf_filemap_fault(struct pt_regs *ctx)
{
	struct vm_fault *vm = (struct vm_fault *)PT_REGS_PARM1(ctx);
	struct vm_area_struct *vma = BPF_CORE_READ(vm, vma);
	struct io_data *entry	   = NULL;
	struct io_key key	   = {};
	struct file *file;
	struct inode *inode;

	file	  = BPF_CORE_READ(vma, vm_file);
	inode	  = BPF_CORE_READ(file, f_inode);
	key.dev	  = BPF_CORE_READ(inode, i_sb, s_dev);

	if (!should_process_device(key.dev))
		return 0;

	key.inode = BPF_CORE_READ(inode, i_ino);
	entry = get_file_io_entry(&key, file);
	if (!entry)
		return 0;
	entry->fs_read_bytes += PAGE_SIZE;

	return 0;
}

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(struct iotracing_schedule_delay_event));
	__uint(max_entries, 128);
} io_schedule_stack SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(int));
} iodelay_perf_events SEC(".maps");

static __always_inline int detect_io_schedule(struct pt_regs *ctx)
{
	struct iotracing_schedule_delay_event entry = {};
	u64 id = bpf_get_current_pid_tgid();
	u32 tid = (u32)id;

	entry.start_ns = bpf_ktime_get_ns();
	bpf_get_current_comm(entry.comm, COMPAT_TASK_COMM_LEN);

	entry.stack_size = bpf_get_stack(ctx, entry.stack, sizeof(entry.stack),
					 0);
	bpf_map_update_elem(&io_schedule_stack, &tid, &entry, COMPAT_BPF_ANY);

	return 0;
}

SEC("kprobe/io_schedule")
int bpf_io_schedule(struct pt_regs *ctx)
{
	return detect_io_schedule(ctx);
}

SEC("kprobe/io_schedule_timeout")
int bpf_io_schedule_timeout(struct pt_regs *ctx)
{
	return detect_io_schedule(ctx);
}

static __always_inline int detect_io_schedule_return(struct pt_regs *ctx)
{
	struct iotracing_schedule_delay_event *entry;
	u64 id = bpf_get_current_pid_tgid();
	u32 tid = (u32)id;
	u64 now = bpf_ktime_get_ns();

	entry = bpf_map_lookup_elem(&io_schedule_stack, &tid);
	if (!entry)
		return 0;

	if (now - entry->start_ns > FILTER_EVENT_TIMEOUT) {
		entry->tgid = id >> 32;
		entry->tid = tid;
		entry->cpu = bpf_get_smp_processor_id();
		entry->duration_ns = now - entry->start_ns;
		bpf_perf_event_output(
			ctx, &iodelay_perf_events, COMPAT_BPF_F_CURRENT_CPU,
			entry, sizeof(struct iotracing_schedule_delay_event));
	}
	bpf_map_delete_elem(&io_schedule_stack, &tid);

	return 0;
}

SEC("kretprobe/io_schedule")
int bpf_return_io_schedule(struct pt_regs *ctx)
{
	return detect_io_schedule_return(ctx);
}

SEC("kretprobe/io_schedule_timeout")
int bpf_return_io_schedule_timeout(struct pt_regs *ctx)
{
	return detect_io_schedule_return(ctx);
}

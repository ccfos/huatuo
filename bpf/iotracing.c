#include "vmlinux.h"

#include <bpf/bpf_core_read.h>
#include <bpf/bpf_helpers.h>
#include <bpf/bpf_tracing.h>

#include "bpf_common.h"

char __license[] SEC("license") = "Dual MIT/GPL";

#define FILEPATH_MAX_DEPTH 8
#define DNAME_INLINE_LEN 32
#define PAGE_SIZE 4096

// Device filter configuration
volatile const u32 FILTER_DEVS[16]	= {};
volatile const u32 FILTER_DEV_COUNT	= 0;
volatile const u64 FILTER_EVENT_TIMEOUT = 100000000;

/*
 * Check if device should be filtered
 * Returns 1 if device should be processed, 0 if should be filtered out
 */
static __always_inline int should_process_device(u32 dev)
{
	if (FILTER_DEV_COUNT == 0)
		return 1;

	for (int i = 0; i < FILTER_DEV_COUNT && i < 16; i++)
		if (FILTER_DEVS[i] == dev)
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
	u32 pid;
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
	u32 pid;
	u32 dev;
	u64 data_len;
	struct blkcg_gq *bi_blkg;
	char comm[COMPAT_TASK_COMM_LEN];
};

struct io_data {
	u32 tgid;
	u32 pid;
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
#define REQ_OP_MASK ((1 << REQ_OP_BITS) - 1)
#define REQ_META (1ULL << __REQ_META)

static __always_inline int is_write_request(u32 cmd_flags)
{
	return (cmd_flags & REQ_OP_MASK) == REQ_OP_WRITE;
}

struct request_queue___new {
	struct gendisk *disk;
};

struct block_device___new {
	dev_t bd_dev;
};

/*
 * compatible with different kernel versions of disk device acquisition
 */
static __always_inline struct gendisk *get_request_disk(struct request *req)
{
	if (bpf_core_field_exists(req->rq_disk)) {
		return BPF_CORE_READ(req, rq_disk);
	} else {
		struct request_queue___new *q;

		q = (struct request_queue___new *)BPF_CORE_READ(req, q);
		return BPF_CORE_READ(q, disk);
	}
}

/*
 * compatible with different kernel versions of partition number acquisition
 */
static __always_inline int get_partition_number(struct request *req)
{
	void *part = BPF_CORE_READ(req, part);

	if (bpf_core_field_exists(((struct hd_struct *)part)->partno)) {
		return BPF_CORE_READ((struct hd_struct *)part, partno);
	} else {
		struct block_device___new *new_part;
		int partno;

		new_part = (struct block_device___new *)part;
		partno	 = BPF_CORE_READ(new_part, bd_dev);
		return partno & 0xff;
	}
}

SEC("kprobe/rq_qos_issue")
int bpf_rq_qos_issue(struct pt_regs *ctx)
{
	struct request *req	  = (struct request *)PT_REGS_PARM2(ctx);
	struct hash_key key	  = {};
	struct io_start_info info = {};
	struct bio *bio;
	struct inode *inode;
	struct gendisk *disk;
	u32 cmd_flags;
	int partno;
	int devn[2];

	bio = BPF_CORE_READ(req, bio);

	cmd_flags = BPF_CORE_READ(req, cmd_flags);
	if (cmd_flags & REQ_META)
		return 0;

	disk = get_request_disk(req);
	/* gendisk.major, gendisk.first_minor */
	if (bpf_probe_read(devn, sizeof(devn), disk))
		return -1;

	partno	   = get_partition_number(req);
	key.dev	   = (devn[0] & 0xfff) << 20 | (devn[1] & 0xff) + partno;
	key.sector = BPF_CORE_READ(req, __sector);

	if (!should_process_device(key.dev))
		return 0;

	inode	   = BPF_CORE_READ(bio, bi_io_vec, bv_page, mapping, host);
	info.inode = BPF_CORE_READ(inode, i_ino);
	if (info.inode == 0)
		info.dev = key.dev;
	else
		info.dev = BPF_CORE_READ(inode, i_sb, s_dev);

	info.pid      = bpf_get_current_pid_tgid() >> 32;
	info.bi_blkg  = BPF_CORE_READ(bio, bi_blkg);
	info.data_len = BPF_CORE_READ(req, __data_len);
	bpf_get_current_comm(info.comm, COMPAT_TASK_COMM_LEN);
	bpf_map_update_elem(&start_info_map, &key, &info, COMPAT_BPF_ANY);

	return 0;
}

SEC("kprobe/rq_qos_done")
int bpf_rq_qos_done(struct pt_regs *ctx)
{
	struct request *req	   = (struct request *)PT_REGS_PARM2(ctx);
	struct io_start_info *info = NULL;
	struct hash_key info_key   = {};
	struct io_key io_key	   = {};
	struct io_data data	   = {};
	struct io_data *entry;
	struct gendisk *disk;
	u32 cmd_flags;
	int partno;
	int devn[2];
	u64 now;

	disk = get_request_disk(req);
	/* gendisk.major, gendisk.first_minor */
	if (bpf_probe_read(devn, sizeof(devn), disk))
		return -1;

	partno		= get_partition_number(req);
	info_key.dev	= (devn[0] & 0xfff) << 20 | (devn[1] & 0xff) + partno;
	info_key.sector = BPF_CORE_READ(req, __sector);

	if (!should_process_device(info_key.dev))
		return 0;

	info = bpf_map_lookup_elem(&start_info_map, &info_key);
	if (!info)
		return 0;

	io_key.dev   = info->dev;
	io_key.inode = info->inode;
	/* for direct IO, set pid value in key */
	if (io_key.inode == 0)
		io_key.pid = info->pid;

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
	entry->latency.sum_q2c += now - BPF_CORE_READ(req, start_time_ns);
	entry->latency.sum_d2c += now - BPF_CORE_READ(req, io_start_time_ns);
	entry->latency.cnt++;

	if (entry == &data) {
		entry->blkcg_gq = (u64)info->bi_blkg;
		entry->pid	= info->pid;
		entry->dev	= info->dev;
		entry->inode	= info->inode;
		bpf_probe_read_str(entry->comm, COMPAT_TASK_COMM_LEN,
				   info->comm);
		bpf_map_update_elem(&io_source_map, &io_key, &data,
				    COMPAT_BPF_ANY);
	}
	bpf_map_delete_elem(&start_info_map, &info_key);

	return 0;
}

static __always_inline void
init_io_data(struct io_data *entry, struct dentry *root_dentry,
	     struct dentry *dentry, struct inode *inode)
{
	u64 t = bpf_get_current_pid_tgid();

	entry->pid  = t >> 32;
	entry->tgid = t & 0xffffffff;

	bpf_get_current_comm(entry->comm, COMPAT_TASK_COMM_LEN);
	for (int i = 0; i < FILEPATH_MAX_DEPTH; i++) {
		if (dentry == NULL)
			break;

		entry->filepath[i][0] = 0;
		bpf_probe_read_str(entry->filepath[i], DNAME_INLINE_LEN,
				   BPF_CORE_READ(dentry, d_name.name));
		if (entry->filepath[i][0] == 0)
			break;
		if (entry->filepath[i][DNAME_INLINE_LEN - 2] != 0) {
			entry->filepath[i][DNAME_INLINE_LEN - 2] = '.';
			entry->filepath[i][DNAME_INLINE_LEN - 3] = '.';
			entry->filepath[i][DNAME_INLINE_LEN - 4] = '.';
		}
		dentry = BPF_CORE_READ(dentry, d_parent);
	}
}

struct iov_iter___new {
	bool data_source;
} __attribute__((preserve_access_index));

static __always_inline int bpf_file_read_write(struct pt_regs *ctx)
{
	struct kiocb *iocb    = (struct kiocb *)PT_REGS_PARM1(ctx);
	struct io_data data   = {};
	struct io_data *entry = NULL;
	struct dentry *dentry;
	struct dentry *root_dentry;
	struct inode *inode;
	struct io_key key = {};
	struct iov_iter *from;
	size_t count;
	unsigned int type;

	inode	  = BPF_CORE_READ(iocb, ki_filp, f_inode);
	key.inode = BPF_CORE_READ(inode, i_ino);
	key.dev	  = BPF_CORE_READ(inode, i_sb, s_dev);

	if (!should_process_device(key.dev))
		return 0;

	entry = bpf_map_lookup_elem(&io_source_map, &key);
	if (!entry)
		entry = &data;

	dentry	    = BPF_CORE_READ(iocb, ki_filp, f_path.dentry);
	root_dentry = BPF_CORE_READ(iocb, ki_filp, f_path.mnt, mnt_root);
	if (entry->tgid == 0) {
		init_io_data(entry, root_dentry, dentry, inode);
		entry->dev   = key.dev;
		entry->inode = key.inode;
	}

	from  = (struct iov_iter *)PT_REGS_PARM2(ctx);
	count = BPF_CORE_READ(from, count);

	if (bpf_core_field_exists(from->type)) {
		type = BPF_CORE_READ(from, type);
	} else {
		struct iov_iter___new *from_new;

		from_new = (struct iov_iter___new *)from;
		type	 = BPF_CORE_READ(from_new, data_source);
	}

	type = type & 0x1;
	if (type) /* 0: read, 1: write */
		entry->fs_write_bytes += count;
	else
		entry->fs_read_bytes += count;

	entry->flag = BPF_CORE_READ(iocb, ki_flags);
	if (entry == &data)
		bpf_map_update_elem(&io_source_map, &key, &data,
				    COMPAT_BPF_ANY);

	return 0;
}

SEC("kprobe/anyfs_file_read_iter")
int bpf_anyfs_file_read_iter(struct pt_regs *ctx)
{
	return bpf_file_read_write(ctx);
}

SEC("kprobe/anyfs_file_write_iter")
int bpf_anyfs_file_write_iter(struct pt_regs *ctx)
{
	return bpf_file_read_write(ctx);
}

static __always_inline int bpf_filemap_page_mkwrite(struct pt_regs *ctx)
{
	struct vm_fault *vm	   = (struct vm_fault *)PT_REGS_PARM1(ctx);
	struct vm_area_struct *vma = BPF_CORE_READ(vm, vma);
	struct io_data *entry	   = NULL;
	struct io_data data	   = {};
	struct io_key key	   = {};
	struct inode *inode;

	inode	  = BPF_CORE_READ(vma, vm_file, f_inode);
	key.inode = BPF_CORE_READ(inode, i_ino);
	key.dev	  = BPF_CORE_READ(inode, i_sb, s_dev);

	if (!should_process_device(key.dev))
		return 0;

	entry = bpf_map_lookup_elem(&io_source_map, &key);
	if (!entry)
		entry = &data;

	if (entry->tgid == 0) {
		struct dentry *dentry;
		struct dentry *root_dentry;

		dentry	    = BPF_CORE_READ(vma, vm_file, f_path.dentry);
		root_dentry = BPF_CORE_READ(vma, vm_file, f_path.mnt, mnt_root);
		init_io_data(entry, root_dentry, dentry, inode);
		entry->dev   = key.dev;
		entry->inode = key.inode;
	}

	entry->fs_write_bytes += PAGE_SIZE;
	if (entry == &data)
		bpf_map_update_elem(&io_source_map, &key, &data,
				    COMPAT_BPF_ANY);

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
	struct vm_fault *vm	   = (struct vm_fault *)PT_REGS_PARM1(ctx);
	struct vm_area_struct *vma = BPF_CORE_READ(vm, vma);
	struct io_data *entry	   = NULL;
	struct io_data data	   = {};
	struct io_key key	   = {};
	struct inode *inode;

	inode	  = BPF_CORE_READ(vma, vm_file, f_inode);
	key.inode = BPF_CORE_READ(inode, i_ino);
	key.dev	  = BPF_CORE_READ(inode, i_sb, s_dev);

	if (!should_process_device(key.dev))
		return 0;

	entry = bpf_map_lookup_elem(&io_source_map, &key);
	if (!entry)
		entry = &data;

	if (entry->tgid == 0) {
		struct dentry *dentry;
		struct dentry *root_dentry;

		dentry	    = BPF_CORE_READ(vma, vm_file, f_path.dentry);
		root_dentry = BPF_CORE_READ(vma, vm_file, f_path.mnt, mnt_root);
		init_io_data(entry, root_dentry, dentry, inode);
		entry->dev   = key.dev;
		entry->inode = key.inode;
	}
	entry->fs_read_bytes += PAGE_SIZE;

	if (entry == &data)
		bpf_map_update_elem(&io_source_map, &key, &data,
				    COMPAT_BPF_ANY);

	return 0;
}

struct iodelay_entry {
	u64 stack[PERF_MIN_STACK_DEPTH];
	u64 ts;
	u64 cost;
	int stack_size;
	u32 pid;
	u32 tid;
	u32 cpu;
	char comm[COMPAT_TASK_COMM_LEN];
};

struct {
	__uint(type, BPF_MAP_TYPE_HASH);
	__uint(key_size, sizeof(u32));
	__uint(value_size, sizeof(struct iodelay_entry));
	__uint(max_entries, 128);
} io_schedule_stack SEC(".maps");

struct {
	__uint(type, BPF_MAP_TYPE_PERF_EVENT_ARRAY);
	__uint(key_size, sizeof(int));
	__uint(value_size, sizeof(int));
} iodelay_perf_events SEC(".maps");

static __always_inline int detect_io_schedule(struct pt_regs *ctx)
{
	struct iodelay_entry entry = {};
	u64 id			   = bpf_get_current_pid_tgid();
	u32 pid			   = id & 0xffffffff;

	entry.ts = bpf_ktime_get_ns();
	bpf_get_current_comm(entry.comm, COMPAT_TASK_COMM_LEN);

	entry.stack_size =
	    bpf_get_stack(ctx, entry.stack, sizeof(entry.stack), 0);
	bpf_map_update_elem(&io_schedule_stack, &pid, &entry, COMPAT_BPF_ANY);

	return 0;
}

SEC("kprobe/io_schedule")
int bpf_io_schedule(struct pt_regs *ctx) { return detect_io_schedule(ctx); }

SEC("kprobe/io_schedule_timeout")
int bpf_io_schedule_timeout(struct pt_regs *ctx)
{
	return detect_io_schedule(ctx);
}

static __always_inline int detect_io_schedule_return(struct pt_regs *ctx)
{
	struct iodelay_entry *entry;
	u64 id	= bpf_get_current_pid_tgid();
	u32 pid = id & 0xffffffff;
	u64 now = bpf_ktime_get_ns();

	entry = bpf_map_lookup_elem(&io_schedule_stack, &pid);
	if (!entry)
		return 0;

	if (now - entry->ts > FILTER_EVENT_TIMEOUT) {
		entry->pid  = (id >> 32) & 0xffffffff;
		entry->tid  = pid;
		entry->cost = now - entry->ts;
		bpf_perf_event_output(ctx, &iodelay_perf_events,
				      COMPAT_BPF_F_CURRENT_CPU, entry,
				      sizeof(struct iodelay_entry));
	}
	bpf_map_delete_elem(&io_schedule_stack, &pid);

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

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

/* Native layout and admission checks call the production BPF functions with
 * bounded map storage and helper return values instead of real kernel objects.
 * Live CO-RE/verifier compatibility is checked by the CLI integration cases.
 */
#define kiocb kiocb_header
#define inode inode_header
#define file file_header
#define vm_area_struct vm_area_struct_header
#define vm_fault vm_fault_header
#include "vmlinux.h"
#undef kiocb
#undef inode
#undef file
#undef vm_area_struct
#undef vm_fault

#ifndef __has_builtin
#define __has_builtin(builtin) 0
#endif

#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>
#include <errno.h>

#ifndef BPF_NOEXIST
#define BPF_NOEXIST 1
#endif

/* Older libbpf declaration pragmas cannot be parsed by GCC. */
#undef SEC
#define SEC(name) __attribute__((section(name), used))

struct inode {
	struct super_block *i_sb;
	u64 padding[TEST_INODE_PADDING];
	u64 i_ino;
	dev_t i_rdev;
};

struct file {
	struct inode *f_inode;
	u64 padding[TEST_FILE_PADDING];
	u32 f_flags;
	struct path f_path;
};

struct kiocb {
	struct file *ki_filp;
	u64 padding[3];
	int ki_flags;
};

struct vm_area_struct {
	struct file *vm_file;
};

struct vm_fault {
	struct vm_area_struct *vma;
};

#define __builtin_preserve_access_index(field) (field)
#define __builtin_preserve_field_info(field, kind) 1
#undef bpf_core_field_offset
#define bpf_core_field_offset(field) ((u64)&(field))

static void *test_lookup(void *map, const void *key);
static long test_update(void *map, const void *key, const void *value, u64 flags);
static long test_read(void *dst, u32 size, const void *src);
static long test_read_str(void *dst, u32 size, const void *src);
static u64 test_pid(void);
static long test_comm(void *dst, u32 size);
#define bpf_map_lookup_elem test_lookup
#define bpf_map_update_elem test_update
#define bpf_probe_read test_read
#define bpf_probe_read_kernel test_read
#define bpf_probe_read_str test_read_str
#define bpf_probe_read_kernel_str test_read_str
#define bpf_get_current_pid_tgid test_pid
#define bpf_get_current_comm test_comm
/* Permit controlled .rodata inputs in this native translation unit. */
#define const
#include "iotracing.c"
#undef const
#undef bpf_map_lookup_elem
#undef bpf_map_update_elem
#undef bpf_probe_read
#undef bpf_probe_read_kernel
#undef bpf_probe_read_str
#undef bpf_probe_read_kernel_str
#undef bpf_get_current_pid_tgid
#undef bpf_get_current_comm

extern int printf(const char *format, ...);

#define TEST_CAPACITY 2

static struct io_key keys[TEST_CAPACITY];
static struct io_data records[TEST_CAPACITY];
static u32 occupied, saturated, update_calls;

static int key_equal(const struct io_key *left, const struct io_key *right)
{
	return left->tgid == right->tgid && left->dev == right->dev &&
	       left->inode == right->inode;
}

static void *test_lookup(void *map, const void *key)
{
	if (map == &io_source_full)
		return *(const u32 *)key == 0 ? &saturated : NULL;
	if (map != &io_source_map)
		return NULL;
	for (u32 index = 0; index < occupied; index++)
		if (key_equal(key, &keys[index]))
			return &records[index];
	return NULL;
}

static long test_update(void *map, const void *key, const void *value, u64 flags)
{
	if (map != &io_source_map)
		return -EINVAL;
	update_calls++;
	for (u32 index = 0; index < occupied; index++) {
		if (!key_equal(key, &keys[index]))
			continue;
		if (flags == BPF_NOEXIST)
			return -EEXIST;
		records[index] = *(const struct io_data *)value;
		return 0;
	}
	if (occupied == TEST_CAPACITY)
		return -E2BIG;
	keys[occupied] = *(const struct io_key *)key;
	records[occupied++] = *(const struct io_data *)value;
	return 0;
}

static long test_read(void *dst, u32 size, const void *src)
{
	char *out = dst;
	const char *in = src;

	if (!src) {
		__builtin_memset(dst, 0, size);
		return -EFAULT;
	}
	for (u32 i = 0; i < size; i++)
		out[i] = in[i];
	return 0;
}

static long test_read_str(void *dst, u32 size, const void *src)
{
	char *out = dst;
	const char *in = src;

	if (!src || !size)
		return -EFAULT;
	for (u32 index = 0; index < size; index++) {
		out[index] = in[index];
		if (!in[index])
			return index + 1;
	}
	out[size - 1] = 0;
	return size;
}

static u64 test_pid(void)
{
	return 1ULL << 32;
}

static long test_comm(void *dst, u32 size)
{
	__builtin_memset(dst, 0, size);
	return 0;
}

static void test_arguments(struct pt_regs *regs, void *first, void *second)
{
	/* PT_REGS_PARM* can be read-only views; write the backing fields. */
#ifdef __TARGET_ARCH_arm64
	regs->regs[0] = (u64)first;
	regs->regs[1] = (u64)second;
#else
	regs->di = (u64)first;
	regs->si = (u64)second;
#endif
}

static int test_fault(struct file *file)
{
	struct vm_area_struct vma = { .vm_file = file };
	struct vm_fault fault = { .vma = &vma };
	struct pt_regs regs = {};

	test_arguments(&regs, &fault, NULL);
	return bpf_filemap_fault(&regs);
}

static int test_mkwrite(struct file *file)
{
	struct vm_area_struct vma = { .vm_file = file };
	struct vm_fault fault = { .vma = &vma };
	struct pt_regs regs = {};

	test_arguments(&regs, &fault, NULL);
	return bpf_anyfs_filemap_page_mkwrite(&regs);
}

int main(void)
{
	struct super_block sb = { .s_dev = 0x123456 };
	struct inode inode = { .i_sb = &sb, .i_ino = 0x100000123ULL };
	struct file file = { .f_inode = &inode, .f_flags = 0x4002 };
	struct kiocb iocb = { .ki_filp = &file, .ki_flags = 0x41 };
	struct iov_iter iter = { .count = 513 };
	struct pt_regs regs = {};
	struct io_data *record;

	keys[0] = (struct io_key){ .dev = sb.s_dev, .inode = inode.i_ino };
	records[0] = (struct io_data){ .tgid = 1, .path_initialized = 1 };
	occupied = 1;
	record = &records[0];
	test_arguments(&regs, &iocb, &iter);
	for (int write = 0; write < 2; write++) {
		file.f_flags += write;
		if (bpf_file_read_write(&regs, write) || occupied != 1 || update_calls ||
		    record->fs_read_bytes != 513 || record->fs_write_bytes != 513 * write ||
		    record->flag != file.f_flags) {
			printf("layout: write=%d rows=%u updates=%u bytes=%llu/%llu flags=%x want=%x\n",
			       write, occupied, update_calls,
			       record->fs_read_bytes, record->fs_write_bytes,
			       record->flag, file.f_flags);
			return 1;
		}
	}

	occupied = 0;
	for (u64 hit = 1; hit <= 2; hit++) {
		if (test_mkwrite(&file) || occupied != 1 ||
		    records[0].fs_write_bytes != hit * PAGE_SIZE ||
		    records[0].fs_read_bytes || records[0].tgid != 1 ||
		    records[0].dev != sb.s_dev || records[0].inode != inode.i_ino ||
		    records[0].path_initialized != 1) {
			printf("shared write fault hit=%llu lost file accounting\n", hit);
			return 1;
		}
	}

	occupied = 0;
	FILTER_DEV_COUNT = 1;
	FILTER_DEV_IDS[0] = sb.s_dev;
	for (u64 index = 0; index <= TEST_CAPACITY; index++) {
		inode.i_ino = index + 1;
		if (test_fault(&file) || test_mkwrite(&file))
			return 1;
	}
	struct io_key rejected = { .dev = sb.s_dev, .inode = inode.i_ino };
	if (occupied != TEST_CAPACITY || saturated != 1 || test_lookup(&io_source_map, &rejected)) {
		printf("capacity: rows=%u full=%u updates=%u\n", occupied, saturated, update_calls);
		return 1;
	}
	inode.i_ino = keys[0].inode;
	/* Completion may admit a block-only row before its first file probe. */
	struct io_data seeded = {
		.dev = sb.s_dev,
		.inode = inode.i_ino,
		.block_read_bytes = 8192,
		.block_write_bytes = 16384,
		.blkcg_gq = 0x1234,
		.latency = {
			.cnt = 3,
			.max_d2c = 13,
			.sum_d2c = 23,
			.max_q2c = 17,
			.sum_q2c = 31,
		},
	};
	records[0] = seeded;
	for (u64 hit = 1; hit <= 2; hit++) {
		if (test_fault(&file) || records[0].fs_read_bytes != hit * 4096 ||
		    records[0].fs_write_bytes || records[0].tgid != 1 ||
		    records[0].path_initialized != 1 || records[0].dev != sb.s_dev ||
		    records[0].inode != inode.i_ino ||
		    records[0].block_read_bytes != seeded.block_read_bytes ||
		    records[0].block_write_bytes != seeded.block_write_bytes ||
		    records[0].blkcg_gq != seeded.blkcg_gq ||
		    __builtin_memcmp(&records[0].latency, &seeded.latency,
				     sizeof(seeded.latency)) ||
		    occupied != TEST_CAPACITY || saturated != 1) {
			printf("metadata hit=%llu lost block statistics or file accounting\n",
			       hit);
			return 1;
		}
	}

	occupied = 0;
	saturated = 0;
	update_calls = 0;
	sb.s_dev++;
	for (u64 index = 0; index <= TEST_CAPACITY; index++) {
		inode.i_ino = index + 1;
		if (test_fault(&file) || test_mkwrite(&file))
			return 1;
	}
	if (occupied || saturated || update_calls) {
		printf("filter: foreign rows=%u full=%u updates=%u\n", occupied, saturated, update_calls);
		return 1;
	}
	sb.s_dev = FILTER_DEV_IDS[0];
	if (test_fault(&file) || occupied != 1 || saturated || records[0].fs_read_bytes != 4096) {
		printf("filter rejected the selected device after foreign input\n");
		return 1;
	}
	printf("file layouts, metadata fill, full-map admission and device filtering verified\n");
	return 0;
}

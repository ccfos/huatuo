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

/* Native layout regression for the production read/write path.
 * Real memory with varied field spacing exercises both snapshot and fallback
 * reads. This complements, but cannot replace, live CO-RE/verifier tests.
 */
#define kiocb kiocb_header
#define inode inode_header
#define file file_header
#include "vmlinux.h"
#undef kiocb
#undef inode
#undef file

#ifndef __has_builtin
#define __has_builtin(builtin) 0
#endif

#include <bpf/bpf_helpers.h>
#include <bpf/bpf_core_read.h>

/* Older libbpf declaration pragmas cannot be parsed by GCC. */
#undef SEC
#define SEC(name) __attribute__((section(name), used))

struct inode {
	struct super_block *i_sb;
	u64 padding[TEST_INODE_PADDING];
	u64 i_ino;
};

struct file {
	struct inode *f_inode;
	struct path f_path;
};

struct kiocb {
	struct file *ki_filp;
	u64 padding[TEST_IOCB_PADDING];
	int ki_flags;
};

#define __builtin_preserve_access_index(field) (field)
#define __builtin_preserve_field_info(field, kind) 1
#undef bpf_core_field_offset
#define bpf_core_field_offset(field) ((u64)&(field))

static void *test_lookup(void *map, const void *key);
static long test_read(void *dst, u32 size, const void *src);
#define bpf_map_lookup_elem test_lookup
#define bpf_probe_read test_read
#define bpf_probe_read_kernel test_read
#include "iotracing.c"
#undef bpf_map_lookup_elem
#undef bpf_probe_read
#undef bpf_probe_read_kernel

extern int printf(const char *format, ...);

static struct io_data record = { .tgid = 1, .path_initialized = 1 };
static u32 bad_key;

static void *test_lookup(void *map, const void *key)
{
	const struct io_key *io = key;

	if (map != &io_source_map)
		return NULL;
	if (io->tgid || io->dev != 0x123456 || io->inode != 0x100000123ULL)
		bad_key++;
	return &record;
}

static long test_read(void *dst, u32 size, const void *src)
{
	char *out = dst;
	const char *in = src;

	for (u32 i = 0; i < size; i++)
		out[i] = in[i];
	return 0;
}

int main(void)
{
	struct super_block sb = { .s_dev = 0x123456 };
	struct inode inode = { .i_sb = &sb, .i_ino = 0x100000123ULL };
	struct file file = { .f_inode = &inode };
	struct kiocb iocb = { .ki_filp = &file, .ki_flags = 0x41 };
	struct iov_iter iter = { .count = 513 };
	struct pt_regs regs = {};

	/* PT_REGS_PARM* can return read-only views; initialize the backing fields. */
#ifdef __TARGET_ARCH_arm64
	regs.regs[0] = (u64)&iocb;
	regs.regs[1] = (u64)&iter;
#else
	regs.di = (u64)&iocb;
	regs.si = (u64)&iter;
#endif
	for (int write = 0; write < 2; write++) {
		iocb.ki_flags += write;
		if (bpf_file_read_write(&regs, write) || bad_key ||
		    record.fs_read_bytes != 513 || record.fs_write_bytes != 513 * write ||
		    record.flag != iocb.ki_flags) {
			printf("write=%d bad_key=%u bytes=%llu/%llu flags=%x want=%x\n",
			       write, bad_key,
			       record.fs_read_bytes, record.fs_write_bytes,
			       record.flag, iocb.ki_flags);
			return 1;
		}
	}
	return 0;
}

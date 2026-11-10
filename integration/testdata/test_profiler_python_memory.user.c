// Copyright 2026 The HuaTuo Authors.
// SPDX-License-Identifier: Apache-2.0

#include <stddef.h>
#include <stdlib.h>
#include <sys/mman.h>

#define MIB (1024UL * 1024UL)

// Exported, non-inlined frames let the hybrid test distinguish C from Python.
__attribute__((noinline)) void *memray_test_retain(void)
{
	return malloc(16 * MIB);
}

__attribute__((noinline)) void *memray_test_temporary(void)
{
	return malloc(8 * MIB);
}

__attribute__((noinline)) void *memray_test_map(void)
{
	return mmap(NULL, 16 * MIB, PROT_READ | PROT_WRITE,
		    MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
}

int memray_test_release(void *temporary, void *mapping)
{
	free(temporary);
	return munmap((char *)mapping + 4 * MIB, 8 * MIB);
}

int memray_test_discard(void *retained, void *temporary, void *mapping)
{
	free(retained);
	free(temporary);
	return munmap(mapping, 16 * MIB);
}

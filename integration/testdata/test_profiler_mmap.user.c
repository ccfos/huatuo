// Copyright 2026 The HuaTuo Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Simple mmap workload for native memory profiler testing.
// Continuously allocates and frees memory via mmap/munmap to trigger
// virtual_alloc profiling events.

#include <stdlib.h>
#include <stdio.h>
#include <signal.h>
#include <unistd.h>
#include <sys/mman.h>

#define KEEP_FRAME __attribute__((noinline, noclone))

static volatile unsigned long sink;

// Fixed allocation sizes for verification in test script
// Total: 4096 + 16384 + 65536 + 262144 = 348160 bytes per iteration
#define ALLOC_SIZE_1 4096
#define ALLOC_SIZE_2 16384
#define ALLOC_SIZE_3 65536
#define ALLOC_SIZE_4 262144
#define TOTAL_ALLOC_SIZE (ALLOC_SIZE_1 + ALLOC_SIZE_2 + ALLOC_SIZE_3 + ALLOC_SIZE_4)

// Marker function for symbol matching in profiler output
static KEEP_FRAME void *test_mmap_allocator(size_t size) {
	void *p = mmap(NULL, size, PROT_READ | PROT_WRITE,
		       MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
	if (p == MAP_FAILED) {
		fprintf(stderr, "failed to mmap %zu bytes\n", size);
		exit(1);
	}
	// Touch pages to ensure they're materialized
	for (size_t i = 0; i < size; i += 4096) {
		((char *)p)[i] = 'x';
	}
	return p;
}

static KEEP_FRAME void test_mmap_deallocator(void *p, size_t size) {
	if (p) {
		munmap(p, size);
	}
}

static KEEP_FRAME void test_alloc_free_loop(void) {
	const size_t block_sizes[] = {ALLOC_SIZE_1, ALLOC_SIZE_2, ALLOC_SIZE_3, ALLOC_SIZE_4};
	const int num_sizes = sizeof(block_sizes) / sizeof(block_sizes[0]);
	void *blocks[4] = {NULL, NULL, NULL, NULL};

	// Allocate blocks of varying sizes
	for (int i = 0; i < num_sizes; i++) {
		blocks[i] = test_mmap_allocator(block_sizes[i]);
	}

	// Do some work
	unsigned long acc = 0;
	for (int i = 0; i < 10000; i++) {
		acc += i * 2654435761UL;
	}
	sink += acc;

	// Free all blocks
	for (int i = 0; i < num_sizes; i++) {
		test_mmap_deallocator(blocks[i], block_sizes[i]);
	}
}

static void wait_for_start_signal(void) {
	sigset_t set;
	int signo;

	sigemptyset(&set);
	sigaddset(&set, SIGUSR1);
	if (sigprocmask(SIG_BLOCK, &set, NULL) != 0) {
		perror("sigprocmask");
		exit(1);
	}

	if (sigwait(&set, &signo) != 0) {
		perror("sigwait");
		exit(1);
	}
	if (signo != SIGUSR1) {
		fprintf(stderr, "unexpected signal %d\n", signo);
		exit(1);
	}
}

int main(void) {
	const int iterations = 500;
	size_t actual_allocated_bytes = 0;

	wait_for_start_signal();

	for (int i = 0; i < iterations; i++) {
		test_alloc_free_loop();
		usleep(10000); // 10ms delay between iterations
		actual_allocated_bytes += TOTAL_ALLOC_SIZE;
	}

	fprintf(stderr, "actual_allocated_bytes=%zu\n", actual_allocated_bytes);
	return 0;
}

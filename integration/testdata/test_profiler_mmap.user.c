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
#include <time.h>
#include <unistd.h>
#include <sys/mman.h>

#define KEEP_FRAME __attribute__((noinline, noclone))

static volatile unsigned long sink;

static KEEP_FRAME void *allocate_block(size_t size) {
	void *p = mmap(NULL, size, PROT_READ | PROT_WRITE,
		       MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
	if (p == MAP_FAILED) {
		return NULL;
	}
	// Touch pages to ensure they're materialized
	for (size_t i = 0; i < size; i += 4096) {
		((char *)p)[i] = 'x';
	}
	return p;
}

static KEEP_FRAME void free_block(void *p, size_t size) {
	if (p) {
		munmap(p, size);
	}
}

static KEEP_FRAME void do_alloc_free_loop(void) {
	const size_t block_sizes[] = {4096, 16384, 65536, 262144};
	const int num_sizes = sizeof(block_sizes) / sizeof(block_sizes[0]);
	void *blocks[4] = {NULL, NULL, NULL, NULL};

	// Allocate blocks of varying sizes
	for (int i = 0; i < num_sizes; i++) {
		blocks[i] = allocate_block(block_sizes[i]);
	}

	// Do some work
	unsigned long acc = 0;
	for (int i = 0; i < 10000; i++) {
		acc += i * 2654435761UL;
	}
	sink += acc;

	// Free all blocks
	for (int i = 0; i < num_sizes; i++) {
		free_block(blocks[i], block_sizes[i]);
	}
}

static double monotonic_seconds(void) {
	struct timespec ts;
	clock_gettime(CLOCK_MONOTONIC, &ts);
	return (double)ts.tv_sec + (double)ts.tv_nsec / 1e9;
}

int main(void) {
	const double duration = 30.0;
	const double deadline = monotonic_seconds() + duration;

	while (monotonic_seconds() < deadline) {
		do_alloc_free_loop();
		usleep(10000); // 10ms delay between iterations
	}

	return 0;
}
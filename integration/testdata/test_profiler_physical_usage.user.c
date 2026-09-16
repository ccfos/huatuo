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

#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/mman.h>
#include <unistd.h>

#define KEEP_FRAME __attribute__((noinline, noclone))
#define PAGES_PER_ITERATION 64
#define ITERATIONS 300
#define ITERATION_DELAY_US 10000

static volatile unsigned long sink;
static size_t page_size;

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

static KEEP_FRAME void test_physical_usage_touch_pages(char *buf, size_t size) {
	for (size_t offset = 0; offset < size; offset += page_size) {
		buf[offset] = (char)(offset / page_size);
		sink += (unsigned long)buf[offset];
	}
}

static KEEP_FRAME void test_physical_usage_alloc_free_loop(void) {
	const size_t size = page_size * PAGES_PER_ITERATION;
	char *buf = mmap(NULL, size, PROT_READ | PROT_WRITE,
			 MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
	if (buf == MAP_FAILED) {
		perror("mmap");
		exit(1);
	}
	/* Keep the expected page count independent of transparent huge pages. */
	if (madvise(buf, size, MADV_NOHUGEPAGE) != 0) {
		perror("madvise");
		exit(1);
	}

	test_physical_usage_touch_pages(buf, size);

	if (munmap(buf, size) != 0) {
		perror("munmap");
		exit(1);
	}
}

int main(void) {
	long configured_page_size = sysconf(_SC_PAGESIZE);
	if (configured_page_size <= 0) {
		perror("sysconf");
		return 1;
	}
	page_size = (size_t)configured_page_size;

	wait_for_start_signal();

	for (int i = 0; i < ITERATIONS; i++) {
		test_physical_usage_alloc_free_loop();
		/* Keep proc mappings available while the profiler drains and symbolizes. */
		usleep(ITERATION_DELAY_US);
	}

	fprintf(stderr, "actual_allocated_bytes=%zu\n",
		page_size * ITERATIONS * PAGES_PER_ITERATION);
	return 0;
}

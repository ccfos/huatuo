// Copyright 2026 The HuaTuo Authors.
// SPDX-License-Identifier: Apache-2.0
#include <signal.h>
#include <stdio.h>
#include <sys/mman.h>
#include <time.h>
#include <unistd.h>

static volatile unsigned long sink;
static volatile sig_atomic_t running = 1;

static void stop_workload(int signal_number)
{
	(void)signal_number;
	running = 0;
}

__attribute__((noinline)) static void hot_loop(void)
{
	for (unsigned long i = 0; i < 1000000; i++)
		sink += i * 2654435761UL;
}

int main(void)
{
	if (signal(SIGTERM, stop_workload) == SIG_ERR)
		return 1;
	puts("ready");
	fflush(stdout);
	const struct timespec delay = {.tv_sec = 0, .tv_nsec = 1000000};
	// Keep live mappings so retained-memory snapshots have a stable target.
	void *retained[16] = {0};
	unsigned int next = 0;
	while (running) {
		hot_loop();
		// Pace page faults to keep functional sampling within CI capacity.
		volatile char *p = (volatile char *)mmap(0, 64 * 1024,
			PROT_READ | PROT_WRITE, MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
		if (p == MAP_FAILED)
			return 1;
		for (int i = 0; i < 64 * 1024; i += 4096) {
			p[i] = 1;
			nanosleep(&delay, 0);
		}
		if (retained[next] && munmap(retained[next], 64 * 1024) != 0)
			return 1;
		retained[next] = (void *)p;
		next = (next + 1) % 16;
	}
	return 0;
}

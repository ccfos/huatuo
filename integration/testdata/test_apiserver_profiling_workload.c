// Copyright 2026 The HuaTuo Authors.
// SPDX-License-Identifier: Apache-2.0
#include <pthread.h>
#include <signal.h>
#include <stdio.h>
#include <string.h>
#include <sys/mman.h>
#include <time.h>
#include <unistd.h>

static volatile unsigned long sink;
static volatile sig_atomic_t running = 1;

static void stop_workload(int signal_number)
{
	(void)signal_number;
	__atomic_store_n(&running, 0, __ATOMIC_RELAXED);
}

__attribute__((noinline)) static void hot_loop(void)
{
	for (unsigned long i = 0; i < 1000000; i++)
		sink += i * 2654435761UL;
}

static void *run_cpu_worker(void *arg)
{
	(void)arg;

	hot_loop();
	puts("ready");
	fflush(stdout);
	while (__atomic_load_n(&running, __ATOMIC_RELAXED))
		hot_loop();
	return NULL;
}

int main(int argc, char **argv)
{
	if (argc > 2 || (argc == 2 && strcmp(argv[1], "--cpu-worker") != 0)) {
		fprintf(stderr, "usage: %s [--cpu-worker]\n", argv[0]);
		return 1;
	}
	if (signal(SIGTERM, stop_workload) == SIG_ERR)
		return 1;
	const int cpu_worker = argc == 2;
	pthread_t worker;
	if (cpu_worker) {
		int err = pthread_create(&worker, NULL, run_cpu_worker, NULL);
		if (err != 0) {
			fprintf(stderr, "start CPU worker: %s\n",
				strerror(err));
			return 1;
		}
	} else {
		puts("ready");
		fflush(stdout);
	}
	const struct timespec delay = {.tv_sec = 0, .tv_nsec = 1000000};
	// Keep live mappings so retained-memory snapshots have a stable target.
	void *retained[16] = {0};
	unsigned int next = 0;
	int status = 0;
	while (__atomic_load_n(&running, __ATOMIC_RELAXED)) {
		// Only one thread writes sink; memory pacing must not idle the
		// CPU worker.
		if (!cpu_worker)
			hot_loop();
		// Pace page faults to keep functional sampling within CI
		// capacity.
		volatile char *p = (volatile char *)
			mmap(0, 64 * 1024, PROT_READ | PROT_WRITE,
			     MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
		if (p == MAP_FAILED) {
			perror("map workload memory");
			status = 1;
			break;
		}
		for (int i = 0; i < 64 * 1024; i += 4096) {
			p[i] = 1;
			nanosleep(&delay, 0);
		}
		if (retained[next] && munmap(retained[next], 64 * 1024) != 0) {
			perror("unmap retained workload memory");
			status = 1;
			break;
		}
		retained[next] = (void *)p;
		next = (next + 1) % 16;
	}
	if (cpu_worker) {
		__atomic_store_n(&running, 0, __ATOMIC_RELAXED);
		int err = pthread_join(worker, NULL);
		if (err != 0) {
			fprintf(stderr, "join CPU worker: %s\n", strerror(err));
			return 1;
		}
	}
	return status;
}

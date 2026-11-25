// Copyright 2026 The HuaTuo Authors.
// SPDX-License-Identifier: Apache-2.0
#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif
#include <errno.h>
#include <sched.h>
#include <sys/prctl.h>
#include <sys/wait.h>
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

// Separate symbols let each process produce a recognizable flame graph.
#define LABEL_WORKLOAD(name, expression) \
	__attribute__((noinline)) static void name##_block(void) \
	{ \
		for (unsigned long i = 1; i < 1000000; i++) \
			sink = (expression); \
	} \
	__attribute__((noinline)) static void name##_worker(void) \
	{ \
		while (__atomic_load_n(&running, __ATOMIC_RELAXED)) \
			name##_block(); \
	}

LABEL_WORKLOAD(hash, (sink ^ i) * 2654435761UL)
LABEL_WORKLOAD(encode, (sink << 3) ^ (i + 97))
LABEL_WORKLOAD(parse, (sink + i) ^ (sink >> 7))
LABEL_WORKLOAD(compress, (sink >> 1) + (i * 31))

static int run_label_workers(const char *ready_path)
{
	if (signal(SIGINT, stop_workload) == SIG_ERR) {
		perror("install workload signal handler");
		return 1;
	}
	cpu_set_t allowed;
	if (sched_getaffinity(0, sizeof(allowed), &allowed) != 0) {
		perror("read allowed CPUs");
		return 1;
	}
	const char *names[] = {"label-hash", "label-encode", "label-parse", "label-compress"};
	void (*workers[])(void) = {hash_worker, encode_worker, parse_worker, compress_worker};
	pid_t children[4];
	int count = 0, status = 1, ready[2];
	FILE *file;
	if (pipe(ready) != 0) {
		perror("create worker readiness pipe");
		return 1;
	}
	for (int cpu = 0; cpu < CPU_SETSIZE && count < 4; cpu++) {
		if (!CPU_ISSET(cpu, &allowed))
			continue;
		pid_t pid = fork();
		if (pid < 0) {
			perror("fork CPU worker");
			goto shutdown;
		}
		if (pid == 0) {
			close(ready[0]);
			cpu_set_t target;
			CPU_ZERO(&target);
			CPU_SET(cpu, &target);
			if (sched_setaffinity(0, sizeof(target), &target) != 0 ||
			    prctl(PR_SET_NAME, names[count], 0, 0, 0) != 0) {
				perror("configure CPU worker");
				_exit(1);
			}
			printf("worker=%s namespace_pid=%ld cpu=%d path=%s\n",
			       names[count], (long)getpid(), cpu, names[count] + 6);
			fflush(stdout);
			if (write(ready[1], "R", 1) != 1)
				_exit(1);
			close(ready[1]);
			workers[count]();
			_exit(0);
		}
		children[count++] = pid;
	}
	close(ready[1]);
	ready[1] = -1;
	for (int i = 0; i < count; i++) {
		char byte;
		if (read(ready[0], &byte, 1) != 1) {
			fprintf(stderr, "CPU worker failed before readiness\n");
			goto shutdown;
		}
	}
	if (!count) {
		fprintf(stderr, "No allowed CPU is available for the workload\n");
		goto shutdown;
	}
	file = ready_path ? fopen(ready_path, "w") : NULL;
	if (ready_path && !file) {
		perror("create workload readiness file");
		goto shutdown;
	}
	if (file) {
		fprintf(file, "%d\n", count);
		if (fclose(file) != 0) {
			perror("publish workload readiness");
			goto shutdown;
		}
	}
	printf("ready workers=%d\n", count);
	fflush(stdout);
	status = 0;
	while (__atomic_load_n(&running, __ATOMIC_RELAXED)) {
		int child_status;
		pid_t exited = waitpid(-1, &child_status, WNOHANG);
		if (exited == 0) {
			const struct timespec delay = {.tv_sec = 0, .tv_nsec = 100000000};
			nanosleep(&delay, NULL);
			continue;
		}
		if (exited < 0 && errno == EINTR)
			continue;
		if (!__atomic_load_n(&running, __ATOMIC_RELAXED))
			break;
		fprintf(stderr, "CPU worker exited unexpectedly: pid=%ld\n", (long)exited);
		for (int i = 0; i < count; i++)
			if (children[i] == exited)
				children[i] = 0;
		status = 1;
		break;
	}
shutdown:
	if (ready_path)
		unlink(ready_path);
	close(ready[0]);
	if (ready[1] >= 0)
		close(ready[1]);
	for (int i = 0; i < count; i++)
		if (children[i] > 0)
			kill(children[i], SIGTERM);
	for (int i = 0; i < count; i++) {
		if (children[i] <= 0)
			continue;
		while (waitpid(children[i], NULL, 0) < 0 && errno == EINTR) {}
	}
	return status;
}

int main(int argc, char **argv)
{
	const int label_workers = argc >= 2 && strcmp(argv[1], "--label-workers") == 0;
	if ((label_workers && argc > 3) ||
	    (!label_workers && (argc > 2 ||
	     (argc == 2 && strcmp(argv[1], "--cpu-worker") != 0)))) {
		fprintf(stderr, "usage: %s [--cpu-worker | --label-workers [READY_FILE]]\n", argv[0]);
		return 1;
	}
	if (signal(SIGTERM, stop_workload) == SIG_ERR)
		return 1;
	if (label_workers)
		return run_label_workers(argc == 3 ? argv[2] : NULL);
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

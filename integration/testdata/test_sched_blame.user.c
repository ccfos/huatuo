// Copyright 2026 The HuaTuo Authors.
// SPDX-License-Identifier: Apache-2.0

#define _GNU_SOURCE
#include <errno.h>
#include <sched.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

static volatile sig_atomic_t stop_requested;

static void request_stop(int signal_number)
{
	(void)signal_number;
	stop_requested = 1;
}

static unsigned long long monotonic_ns(void)
{
	struct timespec now;
	if (clock_gettime(CLOCK_MONOTONIC, &now)) {
		perror("clock_gettime");
		exit(1);
	}
	return (unsigned long long)now.tv_sec * 1000000000ULL + now.tv_nsec;
}

int main(int argc, char **argv)
{
	char *end;
	long cpu;
	cpu_set_t affinity;
	int target;
	unsigned long long deadline;
	volatile unsigned long work = 0;

	if (argc != 4) {
		fprintf(stderr, "usage: %s CPU target|competitor CONTROL_FILE\n", argv[0]);
		return 1;
	}
	errno = 0;
	cpu = strtol(argv[1], &end, 10);
	if (errno || end == argv[1] || *end || cpu < 0 || cpu >= CPU_SETSIZE)
		return 1;
	target = !strcmp(argv[2], "target");
	if (!target && strcmp(argv[2], "competitor"))
		return 1;
	CPU_ZERO(&affinity);
	CPU_SET(cpu, &affinity);
	if (sched_setaffinity(0, sizeof(affinity), &affinity)) {
		perror("sched_setaffinity");
		return 1;
	}
	// PID 1 in the container needs a handler to accept normal termination.
	signal(SIGTERM, request_stop);
	signal(SIGINT, request_stop);
	deadline = monotonic_ns() + 600ULL * 1000000000ULL;
	while (!stop_requested && monotonic_ns() < deadline) {
		if (!target && access(argv[3], F_OK)) {
			if (errno != ENOENT) {
				perror("competitor control file");
				return 1;
			}
			usleep(10000);
			continue;
		}
		unsigned long long busy_until = monotonic_ns() + 1000000ULL;
		while (!stop_requested && monotonic_ns() < busy_until)
			work++;
		// Force completed slices even when the target is alone on this CPU.
		if (target)
			usleep(1000);
	}
	return 0;
}

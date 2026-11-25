// Copyright 2026 The HuaTuo Authors.
// SPDX-License-Identifier: Apache-2.0

#define _GNU_SOURCE

#include <errno.h>
#include <sched.h>
#include <signal.h>
#include <stdio.h>
#include <string.h>
#include <time.h>

__attribute__((noinline)) static void blocking_wait(void)
{
	struct timespec delay = {.tv_sec = 0, .tv_nsec = 5 * 1000 * 1000};
	while (nanosleep(&delay, &delay) != 0 && errno == EINTR) {
	}
}

__attribute__((noinline)) static void wait_loop(void)
{
	for (int i = 0; i < 6000; i++)
		blocking_wait();
}

int main(int argc, char **argv)
{
	/* Shell controls affinity while stopped and checks the observed CPUs. */
	if (argc == 2 && strcmp(argv[1], "--migrate") == 0) {
		for (int i = 0; i < 3; i++) {
			printf("%d\n", sched_getcpu());
			fflush(stdout);
			raise(SIGSTOP);
		}
		return 0;
	}

	wait_loop();
	return 0;
}

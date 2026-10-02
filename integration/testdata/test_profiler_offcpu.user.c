// Copyright 2026 The HuaTuo Authors.
// SPDX-License-Identifier: Apache-2.0

#include <errno.h>
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

int main(void)
{
	wait_loop();
	return 0;
}

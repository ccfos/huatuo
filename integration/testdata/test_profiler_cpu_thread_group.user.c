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

#include <pthread.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <time.h>

#define KEEP_FRAME __attribute__((noinline, noclone))
#define WORK_DURATION_SECONDS 8

static volatile unsigned long sink;

static double monotonic_seconds(void) {
	struct timespec ts;

	clock_gettime(CLOCK_MONOTONIC, &ts);
	return (double)ts.tv_sec + (double)ts.tv_nsec / 1e9;
}

static KEEP_FRAME void thread_group_cpu_loop(void) {
	const double deadline = monotonic_seconds() + WORK_DURATION_SECONDS;
	unsigned long value = 1;

	while (monotonic_seconds() < deadline) {
		value = value * 1664525UL + 1013904223UL;
		sink = value;
	}
}

static void block_start_signal(void) {
	sigset_t set;

	sigemptyset(&set);
	sigaddset(&set, SIGUSR1);
	if (pthread_sigmask(SIG_BLOCK, &set, NULL) != 0) {
		fprintf(stderr, "failed blocking SIGUSR1\n");
		exit(1);
	}
}

static void wait_for_start_signal(void) {
	sigset_t set;
	int signo;

	sigemptyset(&set);
	sigaddset(&set, SIGUSR1);
	if (sigwait(&set, &signo) != 0 || signo != SIGUSR1) {
		fprintf(stderr, "failed waiting for SIGUSR1\n");
		exit(1);
	}
}

static void *cpu_worker(void *arg) {
	(void)arg;
	wait_for_start_signal();
	thread_group_cpu_loop();
	return NULL;
}

int main(void) {
	pthread_t worker;

	block_start_signal();
	if (pthread_create(&worker, NULL, cpu_worker, NULL) != 0) {
		fprintf(stderr, "pthread_create failed\n");
		return 1;
	}
	if (pthread_join(worker, NULL) != 0) {
		fprintf(stderr, "pthread_join failed\n");
		return 1;
	}
	return 0;
}

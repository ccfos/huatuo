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
#include <sys/mman.h>
#include <unistd.h>

#define ALLOCATION_SIZE (64 * 1024)
#define ITERATIONS 500
#define KEEP_FRAME __attribute__((noinline, noclone))

static KEEP_FRAME void thread_group_alloc_loop(void) {
	for (int i = 0; i < ITERATIONS; i++) {
		void *ptr = mmap(NULL, ALLOCATION_SIZE, PROT_READ | PROT_WRITE,
				 MAP_PRIVATE | MAP_ANONYMOUS, -1, 0);
		if (ptr == MAP_FAILED) {
			perror("mmap");
			exit(1);
		}
		((char *)ptr)[0] = 'x';
		if (munmap(ptr, ALLOCATION_SIZE) != 0) {
			perror("munmap");
			exit(1);
		}
		usleep(10000);
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

static void *allocation_worker(void *arg) {
	(void)arg;
	wait_for_start_signal();
	thread_group_alloc_loop();
	return NULL;
}

int main(void) {
	pthread_t worker;

	block_start_signal();
	if (pthread_create(&worker, NULL, allocation_worker, NULL) != 0) {
		fprintf(stderr, "pthread_create failed\n");
		return 1;
	}
	if (pthread_join(worker, NULL) != 0) {
		fprintf(stderr, "pthread_join failed\n");
		return 1;
	}
	return 0;
}

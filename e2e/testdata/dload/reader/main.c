// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

#define _GNU_SOURCE
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/prctl.h>
#include <time.h>
#include <unistd.h>

#define READERS 4
#define BLOCK_SIZE 4096

static int files[READERS];
static int rounds = 3;
static int started;
static pthread_mutex_t mutex = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t condition = PTHREAD_COND_INITIALIZER;

static long long milliseconds(void)
{
	struct timespec now;
	clock_gettime(CLOCK_MONOTONIC, &now);
	return (long long)now.tv_sec * 1000 + now.tv_nsec / 1000000;
}

static int read_block(int fd)
{
	void *buffer = NULL;
	int err = posix_memalign(&buffer, BLOCK_SIZE, BLOCK_SIZE);
	if (err) {
		fprintf(stderr, "allocate aligned read buffer: %s\n", strerror(err));
		return 1;
	}
	ssize_t size = pread(fd, buffer, BLOCK_SIZE, 0);
	err = errno;
	free(buffer);
	if (size != BLOCK_SIZE) {
		fprintf(stderr, "direct read returned %zd bytes: %s\n", size,
			size < 0 ? strerror(err) : "expected 4096 bytes");
		return size < 0 && (err == EINVAL || err == EOPNOTSUPP) ? 77 : 1;
	}
	return 0;
}

static void *worker(void *argument)
{
	intptr_t index = (intptr_t)argument;
	prctl(PR_SET_NAME, "dload-e2e", 0, 0, 0);
	pthread_mutex_lock(&mutex);
	while (!started)
		pthread_cond_wait(&condition, &mutex);
	pthread_mutex_unlock(&mutex);
	for (int round = 0; round < rounds; round++) {
		long long before = milliseconds();
		if (read_block(files[index]))
			return (void *)1;
		printf("read index=%ld round=%d elapsed_ms=%lld\n", (long)index,
			round, milliseconds() - before);
	}
	close(files[index]);
	return NULL;
}

int main(int argc, char **argv)
{
	if (argc != 3) {
		fprintf(stderr, "usage: %s INPUT GO_FILE | --check INPUT\n", argv[0]);
		return 1;
	}
	if (!strcmp(argv[1], "--check")) {
		int fd = open(argv[2], O_RDONLY | O_DIRECT);
		if (fd < 0) {
			int err = errno;
			perror("open direct read file");
			return err == EINVAL || err == EOPNOTSUPP ? 77 : 1;
		}
		int result = read_block(fd);
		close(fd);
		return result;
	}
	setvbuf(stdout, NULL, _IOLBF, 0);
	prctl(PR_SET_NAME, "dload-e2e", 0, 0, 0);
	pthread_t threads[READERS - 1];
	pthread_attr_t attributes;
	if (pthread_attr_init(&attributes) ||
		pthread_attr_setstacksize(&attributes, 256 * 1024))
		return 1;
	for (intptr_t i = 0; i < READERS; i++) {
		files[i] = open(argv[1], O_RDONLY | O_DIRECT);
		if (files[i] < 0) {
			perror("open direct read file");
			return 1;
		}
		if (i < READERS - 1) {
			int err = pthread_create(&threads[i], &attributes, worker, (void *)i);
			if (err) {
				fprintf(stderr, "create reader: %s\n", strerror(err));
				return 1;
			}
		}
	}
	pthread_attr_destroy(&attributes);
	printf("ready readers=%d rounds=%d flags=O_DIRECT\n", READERS, rounds);
	while (access(argv[2], F_OK))
		usleep(100000);
	pthread_mutex_lock(&mutex);
	started = 1;
	pthread_cond_broadcast(&condition);
	pthread_mutex_unlock(&mutex);
	int failed = worker((void *)(READERS - 1)) != NULL;
	for (int i = 0; i < READERS - 1; i++) {
		void *result = NULL;
		int err = pthread_join(threads[i], &result);
		if (err || result)
			failed = 1;
	}
	printf("complete failed=%d\n", failed);
	return failed;
}

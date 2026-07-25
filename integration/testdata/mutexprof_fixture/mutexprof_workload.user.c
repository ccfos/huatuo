// SPDX-License-Identifier: Apache-2.0

#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <signal.h>
#include <stdio.h>
#include <string.h>
#include <sys/ioctl.h>
#include <unistd.h>

#define HUATUO_MUTEXPROF_IOC_MAGIC 0xb7
#define HUATUO_MUTEXPROF_CONTEND _IO(HUATUO_MUTEXPROF_IOC_MAGIC, 1)
#define WORKER_COUNT 8

static volatile sig_atomic_t stopping;
static volatile sig_atomic_t failed;
static volatile sig_atomic_t failure_errno;

static void stop_workload(int signo)
{
	(void)signo;
	stopping = 1;
}

static void *worker(void *opaque)
{
	const int fd = *(int *)opaque;

	while (!stopping) {
		if (ioctl(fd, HUATUO_MUTEXPROF_CONTEND, 0) == 0)
			continue;
		if (errno == EINTR)
			continue;
		failure_errno = errno;
		failed = 1;
		stopping = 1;
	}
	return NULL;
}

int main(int argc, char **argv)
{
	pthread_t threads[WORKER_COUNT];
	struct sigaction action = {.sa_handler = stop_workload};
	int fd;
	int i;

	if (argc != 2) {
		fprintf(stderr, "usage: %s <device>\n", argv[0]);
		return 2;
	}

	sigemptyset(&action.sa_mask);
	sigaction(SIGTERM, &action, NULL);
	sigaction(SIGINT, &action, NULL);

	fd = open(argv[1], O_RDONLY | O_CLOEXEC);
	if (fd < 0) {
		perror("open fixture device");
		return 1;
	}

	for (i = 0; i < WORKER_COUNT; i++) {
		if (pthread_create(&threads[i], NULL, worker, &fd) == 0)
			continue;
		perror("pthread_create");
		stopping = 1;
		failed = 1;
		break;
	}

	while (!stopping)
		sleep(1);
	for (int joined = 0; joined < i; joined++)
		pthread_join(threads[joined], NULL);
	close(fd);
	if (failed && failure_errno != 0)
		fprintf(stderr, "ioctl failed: %s\n", strerror(failure_errno));
	return failed ? 1 : 0;
}
